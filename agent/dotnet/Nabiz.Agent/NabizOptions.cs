using System.Text.Json;
using System.Text.Json.Serialization;

namespace Nabiz.Agent;

/// <summary>
/// Agent configuration. Read from the <c>nabiz.json</c> generated at build
/// time; environment variables override the file.
/// </summary>
/// <remarks>
/// The precedence is deliberate: the file is the default the developer wrote,
/// the environment variable is what the deployment says is true. In Kubernetes
/// the same image goes to different environments, so editing the file inside
/// the image is not an option.
/// </remarks>
public sealed class NabizOptions
{
    /// <summary>The nabiz collector address (OTLP).</summary>
    [JsonPropertyName("endpoint")]
    public string Endpoint { get; set; } = "http://localhost:4317";

    /// <summary>
    /// The nabiz API address. When set, the agent registers itself and appears
    /// in the UI; diagnostics can then be triggered from there.
    /// </summary>
    [JsonPropertyName("apiUrl")]
    public string ApiUrl { get; set; } = "";

    /// <summary>"grpc" (4317) or "http" (4318).</summary>
    [JsonPropertyName("protocol")]
    public string Protocol { get; set; } = "grpc";

    /// <summary>When empty, the entry assembly's name is used.</summary>
    [JsonPropertyName("serviceName")]
    public string ServiceName { get; set; } = "";

    /// <summary>The service's logical group; prevents name clashes in the topology.</summary>
    [JsonPropertyName("serviceNamespace")]
    public string ServiceNamespace { get; set; } = "";

    /// <summary>Such as prod, staging or dev.</summary>
    [JsonPropertyName("environment")]
    public string Environment { get; set; } = "";

    /// <summary>Sampling ratio between 0.0 and 1.0.</summary>
    [JsonPropertyName("sampleRatio")]
    public double SampleRatio { get; set; } = 1.0;

    /// <summary>When false, the agent never starts.</summary>
    [JsonPropertyName("enabled")]
    public bool Enabled { get; set; } = true;

    /// <summary>
    /// Additional ActivitySource names to listen to.
    /// </summary>
    /// <remarks>
    /// The default list covers common libraries that publish their own
    /// ActivitySource. Listening to them is free: if the library is absent, the
    /// source never emits. That makes database, queue and search calls inside a
    /// method visible without any extra package.
    /// </remarks>
    [JsonPropertyName("additionalSources")]
    public string[] AdditionalSources { get; set; } = DefaultSources;

    /// <summary>The library sources listened to by default.</summary>
    public static readonly string[] DefaultSources =
    {
        "Npgsql",                     // PostgreSQL
        "MySqlConnector",             // MySQL
        "Confluent.Kafka",            // Kafka
        "MassTransit",                // messaging
        "RabbitMQ.Client.Publisher",  // RabbitMQ 7+
        "RabbitMQ.Client.Subscriber",
        "Elastic.Transport",          // Elasticsearch
        "MongoDB.Driver.Core.Extensions.DiagnosticSources",
        "Quartz",                     // scheduled jobs
        "Yarp.ReverseProxy",          // reverse proxy
        "Azure.Core",                 // Azure SDK
        "Microsoft.EntityFrameworkCore",
    };

    /// <summary>Settings for automatic code-level measurement.</summary>
    [JsonPropertyName("codeLevel")]
    public CodeLevelSettings CodeLevel { get; set; } = new();

    /// <summary>
    /// On-demand dump settings (the Nabiz.Agent.Diagnostics package).
    /// </summary>
    [JsonPropertyName("diagnostics")]
    public DiagnosticsSettings Diagnostics { get; set; } = new();

    /// <summary>Headers added to OTLP requests, e.g. for ingress authentication.</summary>
    [JsonPropertyName("headers")]
    public Dictionary<string, string> Headers { get; set; } = new();

    /// <summary>
    /// When false, the SQL text is stripped from spans. Query text can contain
    /// personal data, and keeping it is not always wanted.
    /// </summary>
    [JsonPropertyName("captureDbStatement")]
    public bool CaptureDbStatement { get; set; } = true;

    /// <summary>
    /// Adds the code location (file, line, method) to failing spans.
    /// It only runs on errors; successful requests are unaffected.
    /// </summary>
    [JsonPropertyName("captureCodeLocation")]
    public bool CaptureCodeLocation { get; set; } = true;

    /// <summary>Writes the agent's own diagnostic output to the console.</summary>
    [JsonPropertyName("debug")]
    public bool Debug { get; set; }

    /// <summary>The file the configuration was read from; null when none was found.</summary>
    [JsonIgnore]
    public string? SourceFile { get; internal set; }

    /// <summary>
    /// The <c>codeLevel</c> section of nabiz.json.
    /// </summary>
    public sealed class CodeLevelSettings
    {
        /// <summary>Whether measurement is on when AddNabizCodeLevel() is called.</summary>
        [JsonPropertyName("enabled")]
        public bool Enabled { get; set; } = true;

        /// <summary>Namespace prefixes to measure. When empty, the entry assembly's root.</summary>
        [JsonPropertyName("includeNamespaces")]
        public string[] IncludeNamespaces { get; set; } = Array.Empty<string>();

        /// <summary>Namespace prefixes to exclude.</summary>
        [JsonPropertyName("excludeNamespaces")]
        public string[] ExcludeNamespaces { get; set; } = Array.Empty<string>();

        /// <summary>Whether to measure memory allocated per span.</summary>
        [JsonPropertyName("captureAllocations")]
        public bool CaptureAllocations { get; set; } = true;

        /// <summary>Whether to record the thread id and async thread switch.</summary>
        [JsonPropertyName("captureThread")]
        public bool CaptureThread { get; set; } = true;

        /// <summary>Whether to record parameter types. Values are never recorded.</summary>
        [JsonPropertyName("captureParameterTypes")]
        public bool CaptureParameterTypes { get; set; } = true;
    }

    /// <summary>
    /// The <c>diagnostics</c> section of nabiz.json.
    /// </summary>
    /// <remarks>
    /// A memory dump writes the entire memory of the process to disk:
    /// connection strings, session tokens, personal data and passwords
    /// included. That is why it is OFF by default and does not turn on without
    /// a token.
    /// </remarks>
    public sealed class DiagnosticsSettings
    {
        /// <summary>Whether the dump endpoints are open. Off by default.</summary>
        [JsonPropertyName("enabled")]
        public bool Enabled { get; set; }

        /// <summary>The route prefix the endpoints are mounted at.</summary>
        [JsonPropertyName("path")]
        public string Path { get; set; } = "/nabiz/diag";

        /// <summary>
        /// The value expected in the X-Nabiz-Token header. When empty, the
        /// endpoints never open at all.
        /// </summary>
        [JsonPropertyName("token")]
        public string Token { get; set; } = "";

        /// <summary>Where files are written. When empty, the temp directory.</summary>
        [JsonPropertyName("outputDirectory")]
        public string OutputDirectory { get; set; } = "";

        /// <summary>The maximum number of files kept; older ones are deleted.</summary>
        [JsonPropertyName("maxFiles")]
        public int MaxFiles { get; set; } = 5;

        /// <summary>The longest duration allowed for a CPU profile.</summary>
        [JsonPropertyName("maxCpuSeconds")]
        public int MaxCpuSeconds { get; set; } = 120;

        /// <summary>
        /// The port to report at registration. When 0, it is read from the
        /// first port the application listens on.
        /// </summary>
        [JsonPropertyName("advertisedPort")]
        public int AdvertisedPort { get; set; }

        /// <summary>
        /// The address to report at registration. When empty, nabiz uses the
        /// IP the registration came from — which is the right answer in
        /// Kubernetes. Fill it in when the source IP is not routable back, as
        /// with NAT, a proxy or Docker Desktop.
        /// </summary>
        [JsonPropertyName("advertisedHost")]
        public string AdvertisedHost { get; set; } = "";

        /// <summary>
        /// Whether the produced files can be downloaded over HTTP.
        /// </summary>
        /// <remarks>
        /// While off, the files simply stay on disk and are collected with
        /// tools like kubectl cp. Turning it on means anyone who obtains the
        /// token can download the process's memory.
        /// </remarks>
        [JsonPropertyName("allowDownload")]
        public bool AllowDownload { get; set; }
    }

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNameCaseInsensitive = true,
        ReadCommentHandling = JsonCommentHandling.Skip,
        AllowTrailingCommas = true,
    };

    /// <summary>
    /// Loads the configuration from the file and the environment.
    /// </summary>
    public static NabizOptions Load(string? configFile = null)
    {
        var options = LoadFile(configFile) ?? new NabizOptions();
        options.ApplyEnvironment();
        return options;
    }

    private static NabizOptions? LoadFile(string? configFile)
    {
        foreach (var path in CandidatePaths(configFile))
        {
            if (!File.Exists(path)) continue;
            try
            {
                var parsed = JsonSerializer.Deserialize<NabizOptions>(File.ReadAllText(path), JsonOptions);
                if (parsed is null) continue;
                parsed.SourceFile = Path.GetFullPath(path);
                return parsed;
            }
            catch (Exception ex)
            {
                // A broken config must not stop the application from starting.
                // We carry on with the defaults and report the problem.
                Console.Error.WriteLine($"[nabiz] could not read {path}, using defaults: {ex.Message}");
                return null;
            }
        }
        return null;
    }

    private static IEnumerable<string> CandidatePaths(string? configFile)
    {
        var name = configFile
                   ?? System.Environment.GetEnvironmentVariable("NABIZ_CONFIG")
                   ?? "nabiz.json";

        if (Path.IsPathRooted(name))
        {
            yield return name;
            yield break;
        }
        // The output directory comes first, so the right file is found even
        // when the application is started from a different working directory.
        yield return Path.Combine(AppContext.BaseDirectory, name);
        yield return Path.Combine(Directory.GetCurrentDirectory(), name);
    }

    /// <summary>
    /// Applies the environment variables. NABIZ_* wins; the standard OTEL_*
    /// variables are also read, so pods injected by the nabiz operator need no
    /// extra configuration.
    /// </summary>
    private void ApplyEnvironment()
    {
        Endpoint = Env("NABIZ_ENDPOINT") ?? Env("OTEL_EXPORTER_OTLP_ENDPOINT") ?? Endpoint;
        ApiUrl = Env("NABIZ_API_URL") ?? ApiUrl;
        Protocol = Env("NABIZ_PROTOCOL") ?? NormalizeOtelProtocol(Env("OTEL_EXPORTER_OTLP_PROTOCOL")) ?? Protocol;
        ServiceName = Env("NABIZ_SERVICE_NAME") ?? Env("OTEL_SERVICE_NAME") ?? ServiceName;
        ServiceNamespace = Env("NABIZ_SERVICE_NAMESPACE") ?? ServiceNamespace;
        Environment = Env("NABIZ_ENVIRONMENT") ?? Environment;

        if (double.TryParse(Env("NABIZ_SAMPLE_RATIO") ?? Env("OTEL_TRACES_SAMPLER_ARG"),
                System.Globalization.NumberStyles.Float,
                System.Globalization.CultureInfo.InvariantCulture, out var ratio))
        {
            SampleRatio = ratio;
        }
        if (bool.TryParse(Env("NABIZ_ENABLED"), out var enabled)) Enabled = enabled;
        if (bool.TryParse(Env("NABIZ_DEBUG"), out var debug)) Debug = debug;
        if (bool.TryParse(Env("NABIZ_CAPTURE_DB_STATEMENT"), out var capture)) CaptureDbStatement = capture;
        if (bool.TryParse(Env("NABIZ_CAPTURE_CODE_LOCATION"), out var codeLoc)) CaptureCodeLocation = codeLoc;

        SampleRatio = Math.Clamp(SampleRatio, 0.0, 1.0);
    }

    private static string? Env(string key)
    {
        var value = System.Environment.GetEnvironmentVariable(key);
        return string.IsNullOrWhiteSpace(value) ? null : value.Trim();
    }

    // The OTLP standard says "http/protobuf"; we say "http" for short.
    private static string? NormalizeOtelProtocol(string? value) => value switch
    {
        null => null,
        "grpc" => "grpc",
        "http/protobuf" or "http/json" => "http",
        _ => value,
    };

    /// <summary>Formats the headers the way the OTLP exporter expects.</summary>
    internal string HeadersString() =>
        Headers.Count == 0 ? "" : string.Join(",", Headers.Select(kv => $"{kv.Key}={kv.Value}"));
}
