using System.Text.Json;
using System.Text.Json.Serialization;

namespace Nabiz.Agent;

/// <summary>
/// Agent yapılandırması. Derleme sırasında üretilen <c>nabiz.json</c>'dan
/// okunur; ortam değişkenleri dosyayı ezer.
/// </summary>
/// <remarks>
/// Öncelik sırası bilinçli: dosya geliştiricinin yazdığı varsayılan, ortam
/// değişkeni ise dağıtımın söylediği gerçektir. Kubernetes'te aynı imaj farklı
/// ortamlara gittiği için imajın içindeki dosyayı değiştirmek mümkün değildir.
/// </remarks>
public sealed class NabizOptions
{
    /// <summary>nabiz collector adresi (OTLP).</summary>
    [JsonPropertyName("endpoint")]
    public string Endpoint { get; set; } = "http://localhost:4317";

    /// <summary>"grpc" (4317) ya da "http" (4318).</summary>
    [JsonPropertyName("protocol")]
    public string Protocol { get; set; } = "grpc";

    /// <summary>Boşsa giriş assembly'sinin adı kullanılır.</summary>
    [JsonPropertyName("serviceName")]
    public string ServiceName { get; set; } = "";

    /// <summary>Servisin mantıksal grubu; topolojide ad çakışmasını önler.</summary>
    [JsonPropertyName("serviceNamespace")]
    public string ServiceNamespace { get; set; } = "";

    /// <summary>prod, staging, dev gibi.</summary>
    [JsonPropertyName("environment")]
    public string Environment { get; set; } = "";

    /// <summary>0.0 ile 1.0 arası örnekleme oranı.</summary>
    [JsonPropertyName("sampleRatio")]
    public double SampleRatio { get; set; } = 1.0;

    /// <summary>false ise agent hiç başlamaz.</summary>
    [JsonPropertyName("enabled")]
    public bool Enabled { get; set; } = true;

    /// <summary>Dinlenecek ek ActivitySource adları.</summary>
    [JsonPropertyName("additionalSources")]
    public string[] AdditionalSources { get; set; } = new[] { "Npgsql" };

    /// <summary>OTLP isteklerine eklenecek başlıklar (ingress kimlik doğrulaması vb.).</summary>
    [JsonPropertyName("headers")]
    public Dictionary<string, string> Headers { get; set; } = new();

    /// <summary>
    /// false ise SQL metni span'lerden silinir. Sorgu metni kişisel veri
    /// içerebilir; saklamak her zaman istenmez.
    /// </summary>
    [JsonPropertyName("captureDbStatement")]
    public bool CaptureDbStatement { get; set; } = true;

    /// <summary>
    /// Hatalı span'lere kod konumu (dosya, satır, metot) eklenir.
    /// Yalnızca hatalarda çalışır; başarılı istekler etkilenmez.
    /// </summary>
    [JsonPropertyName("captureCodeLocation")]
    public bool CaptureCodeLocation { get; set; } = true;

    /// <summary>Agent'ın kendi tanılama çıktısını konsola yazar.</summary>
    [JsonPropertyName("debug")]
    public bool Debug { get; set; }

    /// <summary>Yapılandırmanın okunduğu dosya; bulunamadıysa null.</summary>
    [JsonIgnore]
    public string? SourceFile { get; internal set; }

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNameCaseInsensitive = true,
        ReadCommentHandling = JsonCommentHandling.Skip,
        AllowTrailingCommas = true,
    };

    /// <summary>
    /// Yapılandırmayı dosyadan ve ortam değişkenlerinden yükler.
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
                // Bozuk bir config yüzünden uygulama açılmamalı. Varsayılanlarla
                // devam edilir, sorun konsola yazılır.
                Console.Error.WriteLine($"[nabiz] {path} okunamadı, varsayılanlar kullanılıyor: {ex.Message}");
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
        // Çıktı dizini önce gelir: uygulama başka bir çalışma dizininden
        // başlatıldığında da doğru dosya bulunsun.
        yield return Path.Combine(AppContext.BaseDirectory, name);
        yield return Path.Combine(Directory.GetCurrentDirectory(), name);
    }

    /// <summary>
    /// Ortam değişkenlerini uygular. NABIZ_* öncelikli; standart OTEL_*
    /// değişkenleri de okunur, böylece nabiz operator'ının enjekte ettiği
    /// pod'larda ek ayar gerekmez.
    /// </summary>
    private void ApplyEnvironment()
    {
        Endpoint = Env("NABIZ_ENDPOINT") ?? Env("OTEL_EXPORTER_OTLP_ENDPOINT") ?? Endpoint;
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

    // OTLP standardı "http/protobuf" der, biz kısaca "http" diyoruz.
    private static string? NormalizeOtelProtocol(string? value) => value switch
    {
        null => null,
        "grpc" => "grpc",
        "http/protobuf" or "http/json" => "http",
        _ => value,
    };

    /// <summary>Başlıkları OTLP exporter'ın beklediği biçime çevirir.</summary>
    internal string HeadersString() =>
        Headers.Count == 0 ? "" : string.Join(",", Headers.Select(kv => $"{kv.Key}={kv.Value}"));
}
