using System.Diagnostics;
using System.Reflection;
using OpenTelemetry;
using OpenTelemetry.Exporter;
using OpenTelemetry.Resources;
using OpenTelemetry.Trace;

namespace Nabiz.Agent;

/// <summary>
/// The nabiz agent's entry point.
/// </summary>
/// <remarks>
/// You normally do not need to call this: at build time the package injects a
/// <c>[ModuleInitializer]</c> into your project, and <see cref="AutoStart"/>
/// runs by itself when the application starts.
/// </remarks>
public static class NabizAgent
{
    private static readonly object Gate = new();
    private static TracerProvider? _provider;
    private static bool _started;

    /// <summary>Whether the agent is running.</summary>
    public static bool IsRunning => _provider is not null;

    /// <summary>The active configuration; null when the agent has not started.</summary>
    public static NabizOptions? Options { get; private set; }

    /// <summary>
    /// Automatic startup. The generated module initializer calls this.
    /// </summary>
    /// <remarks>
    /// No exception ever escapes here. An observability agent taking down the
    /// application it observes at startup is worse than collecting no telemetry
    /// at all.
    /// </remarks>
    public static void AutoStart()
    {
        try
        {
            Start();
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"[nabiz] the agent could not start, the application is unaffected: {ex.Message}");
        }
    }

    /// <summary>
    /// Starts the agent. A second call is ignored.
    /// </summary>
    public static void Start(NabizOptions? options = null)
    {
        lock (Gate)
        {
            if (_started) return;
            _started = true;

            var opts = options ?? NabizOptions.Load();
            Options = opts;

            if (!opts.Enabled)
            {
                Log(opts, "disabled in the configuration, not started");
                return;
            }

            var serviceName = ResolveServiceName(opts);
            _provider = Build(opts, serviceName);

            // Flush spans still queued when the process shuts down.
            AppDomain.CurrentDomain.ProcessExit += (_, _) => Shutdown();

            Log(opts, $"started — service '{serviceName}', target {opts.Endpoint} ({opts.Protocol}), " +
                      $"sampling {opts.SampleRatio:0.###}, config {opts.SourceFile ?? "(none found, using defaults)"}");
        }
    }

    /// <summary>Flushes the queue and stops the agent.</summary>
    public static void Shutdown()
    {
        lock (Gate)
        {
            if (_provider is null) return;
            try
            {
                _provider.ForceFlush(5000);
                _provider.Dispose();
            }
            catch
            {
                // An error during shutdown has nowhere left to be reported.
            }
            _provider = null;
        }
    }

    private static TracerProvider Build(NabizOptions opts, string serviceName)
    {
        var resource = ResourceBuilder.CreateDefault()
            .AddService(
                serviceName: serviceName,
                serviceNamespace: string.IsNullOrEmpty(opts.ServiceNamespace) ? null : opts.ServiceNamespace,
                serviceVersion: EntryAssemblyVersion(),
                autoGenerateServiceInstanceId: true)
            // Also reads OTEL_RESOURCE_ATTRIBUTES: fields such as k8s.pod.name
            // and k8s.namespace.name, injected by the nabiz operator through the
            // downward API, arrive here and feed the topology's Kubernetes
            // dimension.
            .AddEnvironmentVariableDetector();

        var attributes = new List<KeyValuePair<string, object>>();
        if (!string.IsNullOrEmpty(opts.Environment))
            attributes.Add(new("deployment.environment.name", opts.Environment));
        attributes.Add(new("nabiz.agent.version", AgentVersion()));
        resource.AddAttributes(attributes);

        var builder = Sdk.CreateTracerProviderBuilder()
            .SetResourceBuilder(resource)
            // ParentBased is required: a trace getting the same sampling
            // decision in every service is what prevents half-finished traces.
            .SetSampler(new ParentBasedSampler(new TraceIdRatioBasedSampler(opts.SampleRatio)))
            // RecordException: the exception is added to the span as an event
            // carrying its type, message and stack trace. The answer to "which
            // line blew up" lives inside the trace, with no separate log hunt.
            .AddAspNetCoreInstrumentation(o => o.RecordException = true)
            .AddHttpClientInstrumentation(o => o.RecordException = true)
            // SQL Server / Azure SQL. Hiding the query text is handled in one
            // place, by DropSqlTextProcessor: we do not depend on setting names
            // that differ per provider, and the rule is the same for every
            // database.
            .AddSqlClientInstrumentation()
            // The method-level spans the application opens with NabizTracer.
            .AddSource(NabizTracer.SourceName)
            // The method spans of services wrapped by AddNabizCodeLevel().
            .AddSource(NabizCodeLevel.SourceName);

        if (opts.CaptureCodeLocation) builder.AddProcessor(new CodeLocationProcessor());

        foreach (var source in opts.AdditionalSources ?? Array.Empty<string>())
        {
            if (!string.IsNullOrWhiteSpace(source)) builder.AddSource(source.Trim());
        }

        if (!opts.CaptureDbStatement) builder.AddProcessor(new DropSqlTextProcessor());

        builder.AddOtlpExporter(exporter =>
        {
            exporter.Endpoint = BuildEndpoint(opts);
            exporter.Protocol = opts.Protocol.Equals("http", StringComparison.OrdinalIgnoreCase)
                ? OtlpExportProtocol.HttpProtobuf
                : OtlpExportProtocol.Grpc;

            var headers = opts.HeadersString();
            if (headers.Length > 0) exporter.Headers = headers;

            // Export is batched and in the background: no network call on the
            // request path. If the queue fills, spans are dropped — losing
            // telemetry is preferred over slowing the application down.
            exporter.BatchExportProcessorOptions = new BatchExportProcessorOptions<Activity>
            {
                MaxQueueSize = 8192,
                MaxExportBatchSize = 1024,
                ScheduledDelayMilliseconds = 2000,
                ExporterTimeoutMilliseconds = 10000,
            };
        });

        return builder.Build()!;
    }

    // Over OTLP/HTTP the exporter appends the path itself; gRPC expects the root address.
    private static Uri BuildEndpoint(NabizOptions opts)
    {
        var raw = opts.Endpoint.Trim().TrimEnd('/');
        if (!raw.Contains("://", StringComparison.Ordinal)) raw = "http://" + raw;
        return new Uri(raw);
    }

    private static string ResolveServiceName(NabizOptions opts)
    {
        if (!string.IsNullOrWhiteSpace(opts.ServiceName)) return opts.ServiceName.Trim();
        return Assembly.GetEntryAssembly()?.GetName().Name ?? "unknown_service";
    }

    private static string EntryAssemblyVersion() =>
        Assembly.GetEntryAssembly()?.GetName().Version?.ToString() ?? "0.0.0";

    private static string AgentVersion() =>
        typeof(NabizAgent).Assembly.GetName().Version?.ToString() ?? "0.0.0";

    private static void Log(NabizOptions opts, string message)
    {
        if (opts.Debug) Console.WriteLine($"[nabiz] {message}");
    }
}

/// <summary>
/// Strips the SQL text from spans.
/// </summary>
/// <remarks>
/// Query text can carry personal data through parameter values or inlined
/// literals. The stripping happens before export, inside the agent: the data
/// never reaches the server.
/// </remarks>
internal sealed class DropSqlTextProcessor : BaseProcessor<Activity>
{
    public override void OnEnd(Activity activity)
    {
        activity.SetTag("db.statement", null);
        activity.SetTag("db.query.text", null);
    }
}
