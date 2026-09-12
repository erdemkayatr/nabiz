using System.Diagnostics;
using Microsoft.Diagnostics.NETCore.Client;
using Nabiz.Agent;

namespace Nabiz.Agent.Diagnostics;

/// <summary>Üretilmiş bir tanılama dosyası.</summary>
public sealed record DumpFile(string Id, string Kind, long Bytes, DateTimeOffset CreatedAt)
{
    /// <summary>Dosyanın diskteki tam yolu.</summary>
    public string Path { get; init; } = "";
}

/// <summary>
/// CPU profili ve bellek dump'ı üretir, üretilenleri yönetir.
/// </summary>
/// <remarks>
/// Aynı anda yalnızca bir işlem koşar. İki bellek dump'ı birlikte alınırsa
/// süreç iki kez askıya alınır ve zaten sıkıntıda olan bir uygulamayı
/// büsbütün durdurur.
/// </remarks>
public sealed class DumpStore
{
    private readonly NabizOptions.DiagnosticsSettings _settings;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private readonly string _directory;

    /// <summary>Depoyu kurar ve çıktı dizinini hazırlar.</summary>
    public DumpStore(NabizOptions.DiagnosticsSettings settings)
    {
        _settings = settings;
        _directory = string.IsNullOrWhiteSpace(settings.OutputDirectory)
            ? System.IO.Path.Combine(System.IO.Path.GetTempPath(), "nabiz-dumps")
            : settings.OutputDirectory;
        Directory.CreateDirectory(_directory);
    }

    /// <summary>Çıktı dizini.</summary>
    public string Directory_ => _directory;

    /// <summary>Üretilmiş dosyaları yeniden eskiye sıralı verir.</summary>
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

    /// <summary>Kimliğe göre dosyayı bulur. Yol geçişine izin verilmez.</summary>
    public DumpFile? Find(string id)
    {
        // Kimlik doğrudan dosya adı olarak kullanılıyor; "../" ile dizinin
        // dışına çıkmak mümkün olmamalı.
        if (id.Contains('/') || id.Contains('\\') || id.Contains("..")) return null;
        return List().FirstOrDefault(f => f.Id == id);
    }

    /// <summary>Dosyayı siler.</summary>
    public bool Delete(string id)
    {
        var file = Find(id);
        if (file is null) return false;
        File.Delete(file.Path);
        return true;
    }

    /// <summary>
    /// Belirtilen süre boyunca CPU örneklemesi yapar ve .nettrace üretir.
    /// </summary>
    /// <remarks>
    /// Çıktı ham nettrace'tir; bilerek çözümlenmiyor. PerfView, Visual Studio
    /// ve <c>dotnet-trace convert</c> bu biçimi zaten okuyor; kendi
    /// çözümleyicimizi yazmak, hatalarını da üstlenmek olurdu.
    /// </remarks>
    public async Task<DumpFile> CaptureCpuAsync(int seconds, CancellationToken cancellationToken)
    {
        seconds = Math.Clamp(seconds, 1, Math.Max(1, _settings.MaxCpuSeconds));
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
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

            try
            {
                await Task.Delay(TimeSpan.FromSeconds(seconds), cancellationToken).ConfigureAwait(false);
            }
            finally
            {
                // İstek iptal edilse bile oturumu kapat: açık kalan bir
                // EventPipe oturumu sürekli örnekleme maliyeti demektir.
                session.Stop();
            }
            await copy.ConfigureAwait(false);

            Trim();
            var info = new FileInfo(path);
            return new DumpFile(info.Name, "cpu", info.Length, info.CreationTimeUtc) { Path = info.FullName };
        }
        finally
        {
            _gate.Release();
        }
    }

    /// <summary>Süreç belleğini diske yazar.</summary>
    public async Task<DumpFile> CaptureMemoryAsync(DumpType type, CancellationToken cancellationToken)
    {
        await _gate.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            var path = System.IO.Path.Combine(_directory, $"memory-{type.ToString().ToLowerInvariant()}-{Stamp()}.dmp");
            var stopwatch = Stopwatch.StartNew();

            // WriteDump süreci askıya alır ve senkron bloklar; thread havuzunu
            // tıkamamak için ayrı bir thread'e alıyoruz.
            await Task.Run(() =>
            {
                var client = new DiagnosticsClient(System.Environment.ProcessId);
                client.WriteDump(type, path, logDumpGeneration: false);
            }, CancellationToken.None).ConfigureAwait(false);

            Trim();
            var info = new FileInfo(path);
            Console.WriteLine($"[nabiz] bellek dump'ı alındı: {info.Name} " +
                              $"({info.Length / 1024 / 1024} MB, {stopwatch.ElapsedMilliseconds} ms)");
            return new DumpFile(info.Name, "memory", info.Length, info.CreationTimeUtc) { Path = info.FullName };
        }
        finally
        {
            _gate.Release();
        }
    }

    /// <summary>Dosya sayısı sınırı aşıldığında en eskileri siler.</summary>
    private void Trim()
    {
        var files = List();
        foreach (var old in files.Skip(Math.Max(1, _settings.MaxFiles)))
        {
            try { File.Delete(old.Path); }
            catch (IOException) { /* Kullanımdaysa bir sonraki turda silinir. */ }
        }
    }

    private static string Stamp() => DateTime.UtcNow.ToString("yyyyMMdd-HHmmss");

    private static string KindOf(string extension) => extension == ".nettrace" ? "cpu" : "memory";
}
