using System.Diagnostics;
using System.Linq;
using System.Reflection;

namespace Nabiz.Agent;

/// <summary>
/// A transparent wrapper that measures every method call on a service.
/// </summary>
// NOT sealed: DispatchProxy generates a proxy deriving from this type at
// runtime, and it cannot derive from a sealed class.
internal class NabizTracingProxy : DispatchProxy
{
    private object _target = null!;
    private CodeLevelOptions _options = null!;
    private string _typeName = "";

    // .NET 10 added a Create(Type, Type) overload to DispatchProxy, so looking
    // it up by name is now ambiguous. The generic one is picked out by its
    // signature. The result is cached per type: this code runs on every
    // resolution of a scoped service.
    private static readonly MethodInfo CreateMethod =
        typeof(DispatchProxy)
            .GetMethods(BindingFlags.Public | BindingFlags.Static)
            .Single(m => m.Name == nameof(DispatchProxy.Create)
                         && m.IsGenericMethodDefinition
                         && m.GetGenericArguments().Length == 2
                         && m.GetParameters().Length == 0);

    private static readonly System.Collections.Concurrent.ConcurrentDictionary<Type, MethodInfo> CreateCache = new();

    /// <summary>Builds a proxy for the given interface.</summary>
    internal static object Wrap(Type serviceType, object target, CodeLevelOptions options)
    {
        var create = CreateCache.GetOrAdd(serviceType,
            t => CreateMethod.MakeGenericMethod(t, typeof(NabizTracingProxy)));

        var proxy = create.Invoke(null, null)!;
        var self = (NabizTracingProxy)proxy;
        self._target = target;
        self._options = options;
        self._typeName = target.GetType().Name;
        return proxy;
    }

    // The decision is made once per method and cached: scanning attributes on
    // every call would add our own cost to the very latency we are measuring.
    private readonly System.Collections.Concurrent.ConcurrentDictionary<MethodInfo, string?> _spanNames = new();

    protected override object? Invoke(MethodInfo? method, object?[]? args)
    {
        if (method is null) return null;

        var spanName = _spanNames.GetOrAdd(method, ResolveSpanName);
        if (spanName is null) return InvokeTarget(method, args);   // [NabizIgnore]

        var parent = Activity.Current;
        var activity = NabizCodeLevel.Source.StartActivity(spanName, ActivityKind.Internal);

        // If sampling dropped the span, measure nothing: the cost of a disabled
        // agent should be a single null check.
        if (activity is null) return InvokeTarget(method, args);

        var allocStart = _options.CaptureAllocations ? GC.GetAllocatedBytesForCurrentThread() : 0;
        var threadStart = System.Environment.CurrentManagedThreadId;

        Annotate(activity, method, args, threadStart);

        try
        {
            var result = InvokeTarget(method, args);

            // For methods returning a Task the work is not done yet; close the
            // span when it completes. The original object is returned, so the
            // caller's type does not change.
            if (result is Task task)
            {
                // Hand Activity.Current straight back to the CALLER.
                //
                // The called method ran synchronously up to its first await and
                // opened its own child spans under this activity; from there on
                // the async flow carries the right parent in its own
                // ExecutionContext. But the caller's next line starts here too:
                // if Current still points at this activity, the caller's NEXT
                // sibling call would wrongly become its child. The tree breaks
                // and the sum of self times exceeds the trace duration.
                Activity.Current = parent;

                task.ContinueWith(
                    t => Finish(activity, allocStart, threadStart, t.Exception?.InnerException ?? t.Exception),
                    CancellationToken.None,
                    TaskContinuationOptions.ExecuteSynchronously,
                    TaskScheduler.Default);
                return result;
            }

            // There is no way to await a ValueTask without consuming it, and
            // calling AsTask() would take the result out of the caller's hands.
            // The duration covers only the synchronous part, and the span says
            // so explicitly.
            if (result is ValueTask || (result is not null && IsGenericValueTask(result.GetType())))
            {
                activity.SetTag("nabiz.timing.partial", "value_task_sync_only");
            }

            Finish(activity, allocStart, threadStart, null);
            return result;
        }
        catch (Exception ex)
        {
            Finish(activity, allocStart, threadStart, ex);
            throw;
        }
    }

    /// <summary>
    /// Determines the method's span name, or null when it is not measured.
    /// </summary>
    private string? ResolveSpanName(MethodInfo method)
    {
        // Find the real method on the target: attributes are usually placed on
        // the implementation rather than the interface.
        var implementation = FindImplementation(method);

        if (HasIgnore(method) || HasIgnore(implementation)) return null;
        if (HasIgnore(method.DeclaringType) || HasIgnore(_target.GetType())) return null;

        var trace = implementation?.GetCustomAttribute<NabizTraceAttribute>()
                    ?? method.GetCustomAttribute<NabizTraceAttribute>();
        if (!string.IsNullOrWhiteSpace(trace?.Name)) return trace!.Name;

        return $"{_typeName}.{method.Name}";
    }

    private MethodInfo? FindImplementation(MethodInfo interfaceMethod)
    {
        try
        {
            var map = _target.GetType().GetInterfaceMap(interfaceMethod.DeclaringType!);
            var index = Array.IndexOf(map.InterfaceMethods, interfaceMethod);
            return index >= 0 ? map.TargetMethods[index] : null;
        }
        catch
        {
            // Explicit interface implementations and some generic cases cannot
            // be mapped; we then fall back to the attribute on the interface.
            return null;
        }
    }

    private static bool HasIgnore(MemberInfo? member) =>
        member?.GetCustomAttribute<NabizIgnoreAttribute>() is not null;

    private object? InvokeTarget(MethodInfo method, object?[]? args)
    {
        try
        {
            return method.Invoke(_target, args);
        }
        catch (TargetInvocationException ex) when (ex.InnerException is not null)
        {
            // Unwrap the exception the reflection layer wrapped: the stack
            // trace the application sees should look as if no proxy existed.
            System.Runtime.ExceptionServices.ExceptionDispatchInfo
                .Capture(ex.InnerException).Throw();
            throw; // unreachable
        }
    }

    private void Annotate(Activity activity, MethodInfo method, object?[]? args, int threadId)
    {
        activity.SetTag("code.function.name", method.Name);
        activity.SetTag("code.namespace", _target.GetType().FullName);

        if (_options.CaptureParameterTypes)
        {
            activity.SetTag("code.parameter.count", args?.Length ?? 0);
            var parameters = method.GetParameters();
            if (parameters.Length > 0)
            {
                // Types only. Values can carry personal data, passwords or
                // tokens; they never enter the span.
                activity.SetTag("code.parameter.types",
                    string.Join(", ", parameters.Select(p => p.ParameterType.Name)));
            }
        }
        if (_options.CaptureThread) activity.SetTag("thread.id", threadId);
    }

    private void Finish(Activity activity, long allocStart, int threadStart, Exception? error)
    {
        if (_options.CaptureAllocations)
        {
            var delta = GC.GetAllocatedBytesForCurrentThread() - allocStart;
            // In async methods the continuation can run on a different thread
            // and the counter is per thread; a negative delta is meaningless
            // and is discarded.
            if (delta > 0) activity.SetTag("nabiz.allocated.bytes", delta);
        }
        if (_options.CaptureThread)
        {
            var threadEnd = System.Environment.CurrentManagedThreadId;
            if (threadEnd != threadStart)
            {
                activity.SetTag("thread.end.id", threadEnd);
                activity.SetTag("nabiz.async.thread_switched", true);
            }
        }
        if (error is not null)
        {
            activity.SetStatus(ActivityStatusCode.Error, error.Message);
            activity.AddEvent(new ActivityEvent("exception", tags: new ActivityTagsCollection
            {
                { "exception.type", error.GetType().FullName },
                { "exception.message", error.Message },
                { "exception.stacktrace", error.ToString() },
            }));
        }
        // Stop only changes Current when Activity.Current == activity. We save
        // and restore anyway, because the continuation can run on a different
        // flow: a measuring tool must not pollute the context it measures.
        var saved = Activity.Current;
        activity.Stop();
        if (!ReferenceEquals(Activity.Current, saved)) Activity.Current = saved;
    }

    private static bool IsGenericValueTask(Type type) =>
        type.IsGenericType && type.GetGenericTypeDefinition() == typeof(ValueTask<>);
}
