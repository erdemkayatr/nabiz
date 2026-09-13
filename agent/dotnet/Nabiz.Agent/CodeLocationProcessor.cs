using System.Diagnostics;
using OpenTelemetry;

namespace Nabiz.Agent;

/// <summary>
/// Adds the code location to failing spans.
/// </summary>
/// <remarks>
/// The exception event already carries the stack trace, but that is multi-line
/// text and unreadable in a list view. This processor extracts the first frame
/// belonging to the application and writes it into the <c>code.*</c> fields, so
/// which line of which file is visible at a glance in the trace list.
///
/// It only runs on failing spans: parsing a stack for every span would be
/// expensive enough to distort the very latency we are trying to measure.
/// </remarks>
internal sealed class CodeLocationProcessor : BaseProcessor<Activity>
{
    // Skip framework code: what the user needs is the first frame of their own.
    private static readonly string[] SkipPrefixes =
    {
        "System.", "Microsoft.", "OpenTelemetry.", "Nabiz.Agent.",
    };

    public override void OnEnd(Activity activity)
    {
        if (activity.Status != ActivityStatusCode.Error) return;
        if (activity.GetTagItem("code.function.name") is not null) return;

        var stack = FindStackTrace(activity);
        if (stack is null) return;

        var frame = FirstApplicationFrame(stack);
        if (frame is null) return;

        activity.SetTag("code.stacktrace", stack);
        if (frame.Value.function is not null) activity.SetTag("code.function.name", frame.Value.function);
        if (frame.Value.file is not null) activity.SetTag("code.file.path", frame.Value.file);
        if (frame.Value.line > 0) activity.SetTag("code.line.number", frame.Value.line);
    }

    private static string? FindStackTrace(Activity activity)
    {
        foreach (var ev in activity.Events)
        {
            if (ev.Name != "exception") continue;
            foreach (var tag in ev.Tags)
            {
                if (tag.Key == "exception.stacktrace") return tag.Value as string;
            }
        }
        return null;
    }

    // "   at Shop.Orders.Calculate(Int32 id) in /src/Orders.cs:line 42"
    private static (string? function, string? file, int line)? FirstApplicationFrame(string stack)
    {
        foreach (var raw in stack.Split('\n'))
        {
            var line = raw.Trim();
            if (!line.StartsWith("at ", StringComparison.Ordinal)) continue;

            var body = line[3..];
            if (SkipPrefixes.Any(p => body.StartsWith(p, StringComparison.Ordinal))) continue;

            string? file = null;
            var lineNumber = 0;
            var function = body;

            var inIndex = body.LastIndexOf(" in ", StringComparison.Ordinal);
            if (inIndex > 0)
            {
                function = body[..inIndex];
                var location = body[(inIndex + 4)..];
                var lineIndex = location.LastIndexOf(":line ", StringComparison.Ordinal);
                if (lineIndex > 0)
                {
                    file = location[..lineIndex];
                    int.TryParse(location[(lineIndex + 6)..], out lineNumber);
                }
                else
                {
                    file = location;
                }
            }

            var paren = function.IndexOf('(');
            if (paren > 0) function = function[..paren];
            return (function.Trim(), file, lineNumber);
        }
        return null;
    }
}
