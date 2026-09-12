using System.Diagnostics;
using System.Reflection;
using OpenTelemetry;
using OpenTelemetry.Exporter;
using OpenTelemetry.Resources;
using OpenTelemetry.Trace;

namespace Nabiz.Agent;

/// <summary>
/// nabiz agent'ının giriş noktası.
/// </summary>
/// <remarks>
/// Normalde çağırmanız gerekmez: paket, derleme sırasında projenize bir
/// <c>[ModuleInitializer]</c> enjekte eder ve <see cref="AutoStart"/> uygulama
/// başlarken kendiliğinden çalışır.
/// </remarks>
public static class NabizAgent
{
    private static readonly object Gate = new();
    private static TracerProvider? _provider;
    private static bool _started;

    /// <summary>Agent çalışıyor mu.</summary>
    public static bool IsRunning => _provider is not null;

    /// <summary>Etkin yapılandırma; agent başlamadıysa null.</summary>
    public static NabizOptions? Options { get; private set; }

    /// <summary>
    /// Otomatik başlatma. Üretilen module initializer bunu çağırır.
    /// </summary>
    /// <remarks>
    /// Buradan asla istisna çıkmaz. Bir gözlemlenebilirlik agent'ının izlediği
    /// uygulamayı açılışta düşürmesi, hiç telemetri toplamamaktan kötüdür.
    /// </remarks>
    public static void AutoStart()
    {
        try
        {
            Start();
        }
        catch (Exception ex)
        {
            Console.Error.WriteLine($"[nabiz] agent başlatılamadı, uygulama etkilenmedi: {ex.Message}");
        }
    }

    /// <summary>
    /// Agent'ı başlatır. İkinci çağrı yok sayılır.
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
                Log(opts, "yapılandırmada kapalı, başlatılmadı");
                return;
            }

            var serviceName = ResolveServiceName(opts);
            _provider = Build(opts, serviceName);

            // Süreç kapanırken kuyrukta bekleyen span'ler gönderilsin.
            AppDomain.CurrentDomain.ProcessExit += (_, _) => Shutdown();

            Log(opts, $"başladı — servis '{serviceName}', hedef {opts.Endpoint} ({opts.Protocol}), " +
                      $"örnekleme {opts.SampleRatio:0.###}, config {opts.SourceFile ?? "(bulunamadı, varsayılanlar)"}");
        }
    }

    /// <summary>Kuyruğu boşaltır ve agent'ı durdurur.</summary>
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
                // Kapanış sırasındaki hata raporlanacak bir yere zaten gitmiyor.
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
            // OTEL_RESOURCE_ATTRIBUTES'i de okur: nabiz operator'ının downward
            // API ile enjekte ettiği k8s.pod.name, k8s.namespace.name gibi
            // alanlar buradan gelir ve topolojinin Kubernetes boyutunu besler.
            .AddEnvironmentVariableDetector();

        var attributes = new List<KeyValuePair<string, object>>();
        if (!string.IsNullOrEmpty(opts.Environment))
            attributes.Add(new("deployment.environment.name", opts.Environment));
        attributes.Add(new("nabiz.agent.version", AgentVersion()));
        resource.AddAttributes(attributes);

        var builder = Sdk.CreateTracerProviderBuilder()
            .SetResourceBuilder(resource)
            // ParentBased olması şart: bir trace'in tüm servislerde aynı
            // örnekleme kararını alması, yarım kalmış izlerin önüne geçer.
            .SetSampler(new ParentBasedSampler(new TraceIdRatioBasedSampler(opts.SampleRatio)))
            // RecordException: istisna, span'e tür/mesaj/yığın izi taşıyan bir
            // olay olarak eklenir. "Hangi satırda patladı" sorusunun cevabı
            // trace'in içinde durur, ayrı bir log aramaya gerek kalmaz.
            .AddAspNetCoreInstrumentation(o => o.RecordException = true)
            .AddHttpClientInstrumentation(o => o.RecordException = true)
            // Uygulamanın NabizTracer ile açtığı metot seviyesi span'ler.
            .AddSource(NabizTracer.SourceName)
            // AddNabizCodeLevel() ile sarmalanan servislerin metot span'leri.
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

            // Gönderim arka planda ve toplu: istek yolunda ağ çağrısı yok.
            // Kuyruk dolarsa span düşer — uygulamanın yavaşlaması yerine
            // telemetri kaybı tercih edilir.
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

    // OTLP/HTTP'de exporter yol ekini kendisi koyar, gRPC'de kök adres beklenir.
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
/// SQL metnini span'lerden siler.
/// </summary>
/// <remarks>
/// Sorgu metni parametre değerleri ya da gömülü sabitler yoluyla kişisel veri
/// taşıyabilir. Silme işlemi dışa aktarımdan önce, agent içinde yapılır:
/// veri sunucuya hiç ulaşmaz.
/// </remarks>
internal sealed class DropSqlTextProcessor : BaseProcessor<Activity>
{
    public override void OnEnd(Activity activity)
    {
        activity.SetTag("db.statement", null);
        activity.SetTag("db.query.text", null);
    }
}
