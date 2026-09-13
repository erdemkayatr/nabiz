using System.Diagnostics;
using System.Reflection;
using Microsoft.Extensions.DependencyInjection;

namespace Nabiz.Agent;

/// <summary>
/// Settings for automatic code-level instrumentation.
/// </summary>
public sealed class CodeLevelOptions
{
    /// <summary>
    /// Namespace prefixes of the types to wrap. When empty, the entry
    /// assembly's root namespace is used.
    /// </summary>
    public List<string> IncludeNamespaces { get; } = new();

    /// <summary>Namespace prefixes to exclude.</summary>
    public List<string> ExcludeNamespaces { get; } = new();

    /// <summary>Whether to measure memory allocated per span.</summary>
    public bool CaptureAllocations { get; set; } = true;

    /// <summary>Whether to record the thread id and async thread switch.</summary>
    public bool CaptureThread { get; set; } = true;

    /// <summary>Whether to record the count and types of method parameters.</summary>
    /// <remarks>
    /// Types only; values are never recorded. A parameter value can carry
    /// personal data, a password or a token.
    /// </remarks>
    public bool CaptureParameterTypes { get; set; } = true;
}

/// <summary>
/// Automatically measures the methods of services registered in DI.
/// </summary>
/// <remarks>
/// <para>
/// Tools like Dynatrace rewrite IL at runtime through the CLR Profiler API and
/// see every method with no markup at all. This package does not touch IL:
/// emitting broken IL means an observability tool that crashes the application
/// it observes.
/// </para>
/// <para>
/// Instead, the DI registrations are wrapped. Every method of every service
/// registered through an interface gets its own span with no code change. The
/// price is one line; removing even that with <c>IHostingStartup</c> was tried
/// and does not work: hosting startup runs before the application's own
/// registrations and cannot yet see the services to wrap.
/// </para>
/// </remarks>
public static class NabizCodeLevel
{
    internal const string SourceName = "Nabiz.Agent.Services";
    internal static readonly ActivitySource Source = new(SourceName);

    // Wrapping framework services is both noise and risk: some are called
    // during startup and are not suitable for proxying.
    private static readonly string[] AlwaysExcluded =
    {
        "System.", "Microsoft.", "OpenTelemetry.", "Nabiz.Agent.",
    };

    /// <summary>
    /// Puts the application's services under automatic measurement.
    /// </summary>
    /// <example>
    /// <code>
    /// var builder = WebApplication.CreateBuilder(args);
    /// builder.Services.AddScoped&lt;ICartService, CartService&gt;();
    /// builder.Services.AddNabizCodeLevel();   // AFTER the registrations
    /// </code>
    /// </example>
    public static IServiceCollection AddNabizCodeLevel(
        this IServiceCollection services, Action<CodeLevelOptions>? configure = null)
    {
        // First the codeLevel section of nabiz.json, then the callback in
        // code. Code has the last word: a setting the compiler sees is a
        // clearer statement of intent than one in a file.
        var settings = NabizAgent.Options?.CodeLevel ?? new NabizOptions.CodeLevelSettings();
        var options = new CodeLevelOptions
        {
            CaptureAllocations = settings.CaptureAllocations,
            CaptureThread = settings.CaptureThread,
            CaptureParameterTypes = settings.CaptureParameterTypes,
        };
        options.IncludeNamespaces.AddRange(settings.IncludeNamespaces);
        options.ExcludeNamespaces.AddRange(settings.ExcludeNamespaces);
        configure?.Invoke(options);

        if (!settings.Enabled)
        {
            Log("code-level measurement is disabled in the configuration");
            return services;
        }

        if (options.IncludeNamespaces.Count == 0)
        {
            var root = RootNamespace();
            if (root is not null) options.IncludeNamespaces.Add(root);
        }

        var wrapped = 0;
        var skippedClasses = new List<string>();

        // We modify the list while walking it, so we iterate over a copy.
        foreach (var descriptor in services.ToList())
        {
            if (IsClassRegistrationInScope(descriptor, options))
            {
                skippedClasses.Add(descriptor.ServiceType.Name);
                continue;
            }
            if (!ShouldWrap(descriptor, options)) continue;
            if (Replace(services, descriptor, options)) wrapped++;
        }

        Log($"wrapped {wrapped} services (namespaces: {string.Join(", ", options.IncludeNamespaces)})");

        // Say it rather than skipping quietly: the answer to "why can't I see
        // this service's methods" should be in the log.
        if (skippedClasses.Count > 0)
        {
            Console.Error.WriteLine(
                $"[nabiz] {skippedClasses.Count} services could not be measured because they " +
                $"are registered as concrete classes: {string.Join(", ", skippedClasses.Take(10))}" +
                (skippedClasses.Count > 10 ? " …" : "") +
                ". Register them through an interface, or wait for the IL weaving release.");
        }
        return services;
    }

    // Registrations in scope but without an interface: no proxy can be built.
    private static bool IsClassRegistrationInScope(ServiceDescriptor d, CodeLevelOptions options)
    {
        if (d.ServiceType.IsInterface) return false;
        var name = d.ServiceType.FullName;
        if (name is null) return false;
        if (AlwaysExcluded.Any(p => name.StartsWith(p, StringComparison.Ordinal))) return false;
        if (options.ExcludeNamespaces.Any(p => name.StartsWith(p, StringComparison.Ordinal))) return false;
        return options.IncludeNamespaces.Any(p => name.StartsWith(p, StringComparison.Ordinal));
    }

    private static void Log(string message)
    {
        if (NabizAgent.Options?.Debug == true) Console.WriteLine($"[nabiz] code level: {message}");
    }

    private static string? RootNamespace()
    {
        var name = Assembly.GetEntryAssembly()?.GetName().Name;
        if (string.IsNullOrEmpty(name)) return null;
        var dot = name.IndexOf('.');
        return dot > 0 ? name[..dot] : name;
    }

    private static bool ShouldWrap(ServiceDescriptor d, CodeLevelOptions options)
    {
        // DispatchProxy can only wrap interfaces. Services registered as
        // concrete classes would need a virtual-method proxy, which only works
        // when the methods are virtual and otherwise measures incompletely
        // without saying so.
        if (!d.ServiceType.IsInterface || d.ServiceType.IsGenericTypeDefinition) return false;

        var target = d.ImplementationType ?? d.ImplementationInstance?.GetType();
        var ns = target?.FullName ?? d.ServiceType.FullName;
        if (ns is null) return false;

        if (AlwaysExcluded.Any(p => ns.StartsWith(p, StringComparison.Ordinal))) return false;
        if (options.ExcludeNamespaces.Any(p => ns.StartsWith(p, StringComparison.Ordinal))) return false;
        return options.IncludeNamespaces.Any(p => ns.StartsWith(p, StringComparison.Ordinal));
    }

    private static bool Replace(IServiceCollection services, ServiceDescriptor d, CodeLevelOptions options)
    {
        try
        {
            var index = services.IndexOf(d);
            if (index < 0) return false;

            services[index] = ServiceDescriptor.Describe(
                d.ServiceType,
                provider =>
                {
                    var actual = CreateOriginal(provider, d);
                    return NabizTracingProxy.Wrap(d.ServiceType, actual, options);
                },
                d.Lifetime);
            return true;
        }
        catch (Exception ex)
        {
            // One service failing to wrap must not stop the application from
            // starting. It is skipped and we carry on.
            Console.Error.WriteLine($"[nabiz] could not wrap {d.ServiceType.Name}, skipped: {ex.Message}");
            return false;
        }
    }

    private static object CreateOriginal(IServiceProvider provider, ServiceDescriptor d)
    {
        if (d.ImplementationInstance is not null) return d.ImplementationInstance;
        if (d.ImplementationFactory is not null) return d.ImplementationFactory(provider);
        return ActivatorUtilities.CreateInstance(provider, d.ImplementationType!);
    }
}
