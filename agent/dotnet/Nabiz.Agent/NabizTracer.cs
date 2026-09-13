using System.Diagnostics;
using System.Runtime.CompilerServices;

namespace Nabiz.Agent;

/// <summary>
/// A thin wrapper for method-level timing.
/// </summary>
/// <remarks>
/// Automatic instrumentation sees requests, HTTP calls and database queries,
/// but not your own code in between. Knowing that a request took 200 ms does
/// not tell you where those 200 ms went.
///
/// Real method-level profiling means rewriting IL through the CLR Profiler API,
/// which puts a measurement cost on every single method. The approach here is
/// deliberately selective: you mark the place you want measured, and the file
/// and line come free from the compiler.
/// </remarks>
public static class NabizTracer
{
    /// <summary>The ActivitySource name this wrapper uses.</summary>
    public const string SourceName = "Nabiz.Agent.CodeLevel";

    private static readonly ActivitySource Source = new(SourceName);

    /// <summary>
    /// Measures a block of code. The returned object must be closed with
    /// <c>using</c>.
    /// </summary>
    /// <param name="name">
    /// The span name. When omitted, the calling method's name is used.
    /// </param>
    /// <param name="member">Filled in by the compiler; the calling method's name.</param>
    /// <param name="file">Filled in by the compiler; the calling file's path.</param>
    /// <param name="line">Filled in by the compiler; the call's line number.</param>
    /// <example>
    /// <code>
    /// using var span = NabizTracer.Start();          // named after the method
    /// using var span = NabizTracer.Start("calculate price");
    /// </code>
    /// </example>
    public static NabizSpan Start(
        string? name = null,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        var activity = Source.StartActivity(name ?? member, ActivityKind.Internal);
        if (activity is null) return default;

        // OpenTelemetry code semantic conventions. The nabiz UI reads these
        // fields to show which line of which file a span came from.
        activity.SetTag("code.function.name", member);
        if (!string.IsNullOrEmpty(file))
        {
            activity.SetTag("code.file.path", file);
            activity.SetTag("code.line.number", line);
        }
        return new NabizSpan(activity);
    }

    /// <summary>Measures an action.</summary>
    public static void Measure(
        string name, Action action,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            action();
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }

    /// <summary>Measures a function and returns its result.</summary>
    public static T Measure<T>(
        string name, Func<T> func,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            return func();
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }

    /// <summary>Measures an asynchronous operation.</summary>
    public static async Task<T> MeasureAsync<T>(
        string name, Func<Task<T>> func,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            return await func().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }

    /// <summary>Measures an asynchronous action.</summary>
    public static async Task MeasureAsync(
        string name, Func<Task> func,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            await func().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }
}

/// <summary>
/// The measurement scope returned by <see cref="NabizTracer.Start"/>.
/// </summary>
/// <remarks>
/// Being a struct is deliberate: when the sampling decision drops the span it
/// returns <c>default</c> and nothing is allocated. The cost of a disabled
/// agent comes down to a single null check.
/// </remarks>
public readonly struct NabizSpan : IDisposable
{
    private readonly Activity? _activity;

    internal NabizSpan(Activity? activity) => _activity = activity;

    /// <summary>The underlying Activity; null when the span was not sampled.</summary>
    public Activity? Activity => _activity;

    /// <summary>Adds a tag to the span.</summary>
    public NabizSpan SetTag(string key, object? value)
    {
        _activity?.SetTag(key, value);
        return this;
    }

    /// <summary>Marks the span as failed and records the exception with its stack trace.</summary>
    public NabizSpan Fail(Exception ex)
    {
        if (_activity is null) return this;
        _activity.SetStatus(ActivityStatusCode.Error, ex.Message);
        _activity.AddEvent(new ActivityEvent("exception", tags: new ActivityTagsCollection
        {
            { "exception.type", ex.GetType().FullName },
            { "exception.message", ex.Message },
            { "exception.stacktrace", ex.ToString() },
        }));
        return this;
    }

    /// <inheritdoc />
    public void Dispose() => _activity?.Dispose();
}
