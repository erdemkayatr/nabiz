using System.Diagnostics;
using Microsoft.Diagnostics.NETCore.Client;
using Nabiz.Agent;

namespace Nabiz.Agent.Diagnostics;

/// <summary>A diagnostic file that has been produced.</summary>
public sealed record DumpFile(string Id, string Kind, long Bytes, DateTimeOffset CreatedAt)
{
    /// <summary>The file's full path on disk.</summary>
    public string Path { get; init; } = "";
}

/// <summary>
/// Produces CPU profiles and memory dumps, and manages what it produced.
/// </summary>
/// <remarks>
/// Only one operation runs at a time. Two concurrent memory dumps would suspend
/// the process twice and bring an application that is already in trouble to a
/// complete stop.
/// </remarks>
public sealed class DumpStore
{
    private readonly NabizOptions.DiagnosticsSettings _settings;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly string _directory;

    // So a running operation can be stopped from outside.
    private CancellationTokenSource? _running;
    private string _runningKind = "";

    /// <summary>Builds the store and prepares the output directory.</summary>
    public DumpStore(NabizOptions.DiagnosticsSettings settings)
    {
        _settings = settings;
        _directory = string.IsNullOrWhiteSpace(settings.OutputDirectory)
            ? System.IO.Path.Combine(System.IO.Path.GetTempPath(), "nabiz-dumps")
            : settings.OutputDirectory;
        Directory.CreateDirectory(_directory);
    }

    /// <summary>The output directory.</summary>
    public string Directory_ => _directory;

    /// <summary>The kind of operation currently running; empty when idle.</summary>
    public string RunningKind => _runningKind;

    /// <summary>
    /// Stops the running operation.
    /// </summary>
    /// <returns>
    /// True when the operation really could be interrupted.
    /// </returns>
    /// <remarks>
    /// A CPU profile can be interrupted at any time: the session is closed and
    /// the samples collected so far make a valid file.
    ///
    /// A memory dump cannot. The WriteDump call goes to the runtime, which
    /// suspends the process and writes the file; once that starts there is no
    /// way back. Trying to cut it short risks leaving a suspended process
    /// behind.
    /// </remarks>
    public bool Cancel()
    {
        var cts = _running;
        if (cts is null) return false;
        if (_runningKind == "memory") return false;

        try { cts.Cancel(); return true; }
        catch (ObjectDisposedException) { return false; }
    }

    /// <summary>Returns the produced files, newest first.</summary>
    public IReadOnlyList<DumpFile> List()
    {
        var dir = new DirectoryInfo(_directory);
        if (!dir.Exists) return Array.Empty<DumpFile>();
        return dir.GetFiles()
            .Where(f => f.Extension is ".nettrace" or ".dmp")
            .OrderByDescending(f => f.CreationTimeUtc)
            .Select(f => new DumpFile(f.Name, KindOf(f.Extension), f.Length, f.CreationTimeUtc) { Path = f.FullName })
            .ToList();
    }

    /// <summary>Finds a file by id. Path traversal is not allowed.</summary>
    public DumpFile? Find(string id)
    {
        // The id is used directly as the file name; escaping the directory
        // with "../" must not be possible.
        if (id.Contains('/') || id.Contains('\\') || id.Contains("..")) return null;
        return List().FirstOrDefault(f => f.Id == id);
    }

    /// <summary>Deletes a file.</summary>
    public bool Delete(string id)
    {
        var file = Find(id);
        if (file is null) return false;
        File.Delete(file.Path);
        return true;
    }

    /// <summary>
    /// Samples the CPU for the given duration and produces a .nettrace file.
    /// </summary>
    /// <remarks>
    /// The output is raw nettrace, deliberately not decoded. PerfView, Visual
    /// Studio and <c>dotnet-trace convert</c> already read the format; writing
    /// our own decoder would mean owning its bugs too.
    /// </remarks>
    public async Task<DumpFile> CaptureCpuAsync(int seconds, CancellationToken cancellationToken)
    {
        seconds = Math.Clamp(seconds, 1, Math.Max(1, _settings.MaxCpuSeconds));
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        _running = linked;
        _runningKind = "cpu";
        try
        {
            var path = System.IO.Path.Combine(_directory, $"cpu-{Stamp()}.nettrace");
            var client = new DiagnosticsClient(System.Environment.ProcessId);
            var providers = new[]
            {
                new EventPipeProvider("Microsoft-DotNETCore-SampleProfiler",
                    System.Diagnostics.Tracing.EventLevel.Informational),
            };

            using var session = client.StartEventPipeSession(providers, requestRundown: true);
            var copy = Task.Run(async () =>
            {
                await using var output = File.Create(path);
                await session.EventStream.CopyToAsync(output).ConfigureAwait(false);
            }, CancellationToken.None);

            var stoppedEarly = false;
            try
            {
                await Task.Delay(TimeSpan.FromSeconds(seconds), linked.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                // Stopping early is not an error: the samples collected so far
                // make a valid profile.
                stoppedEarly = true;
            }
            finally
            {
                // Close the session even when cancelled: an EventPipe session
                // left open means a permanent sampling cost.
                session.Stop();
            }
            await copy.ConfigureAwait(false);
            if (stoppedEarly) Console.WriteLine("[nabiz] CPU profile stopped early");
            Trim();
            var info = new FileInfo(path);
            return new DumpFile(info.Name, "cpu", info.Length, info.CreationTimeUtc) { Path = info.FullName };
        }
        finally
        {
            _running = null;
            _runningKind = "";
            _gate.Release();
        }
    }

    /// <summary>Writes the process's memory to disk.</summary>
    public async Task<DumpFile> CaptureMemoryAsync(DumpType type, CancellationToken cancellationToken)
    {
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        _running = linked;
        _runningKind = "memory";
        try
        {
            var path = System.IO.Path.Combine(_directory, $"memory-{type.ToString().ToLowerInvariant()}-{Stamp()}.dmp");
            var stopwatch = Stopwatch.StartNew();

            // WriteDump suspends the process and blocks synchronously; it is
            // moved onto its own thread so it does not clog the thread pool.
            await Task.Run(() =>
            {
                var client = new DiagnosticsClient(System.Environment.ProcessId);
                client.WriteDump(type, path, logDumpGeneration: false);
            }, CancellationToken.None).ConfigureAwait(false);

            Trim();
            var info = new FileInfo(path);
            Console.WriteLine($"[nabiz] memory dump taken: {info.Name} " +
                              $"({info.Length / 1024 / 1024} MB, {stopwatch.ElapsedMilliseconds} ms)");
            return new DumpFile(info.Name, "memory", info.Length, info.CreationTimeUtc) { Path = info.FullName };
        }
        finally
        {
            _running = null;
            _runningKind = "";
            _gate.Release();
        }
    }

    /// <summary>Deletes the oldest files once the file count limit is exceeded.</summary>
    private void Trim()
    {
        var files = List();
        foreach (var old in files.Skip(Math.Max(1, _settings.MaxFiles)))
        {
            try { File.Delete(old.Path); }
            catch (IOException) { /* If it is in use, it goes on the next round. */ }
        }
    }

    private static string Stamp() => DateTime.UtcNow.ToString("yyyyMMdd-HHmmss");

    private static string KindOf(string extension) => extension == ".nettrace" ? "cpu" : "memory";
}
