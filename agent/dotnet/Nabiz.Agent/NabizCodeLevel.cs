using System.Diagnostics;
using System.Reflection;
using Microsoft.Extensions.DependencyInjection;

namespace Nabiz.Agent;

/// <summary>
/// Kod seviyesi otomatik enstrümantasyon ayarları.
/// </summary>
public sealed class CodeLevelOptions
{
    /// <summary>
    /// Sarmalanacak tiplerin namespace önekleri. Boşsa giriş assembly'sinin
    /// kök namespace'i kullanılır.
    /// </summary>
    public List<string> IncludeNamespaces { get; } = new();

    /// <summary>Dışlanacak namespace önekleri.</summary>
    public List<string> ExcludeNamespaces { get; } = new();

    /// <summary>Span başına ayrılan bellek ölçülsün mü.</summary>
    public bool CaptureAllocations { get; set; } = true;

    /// <summary>Thread kimliği ve async geçişi kaydedilsin mi.</summary>
    public bool CaptureThread { get; set; } = true;

    /// <summary>Metot parametrelerinin sayısı ve tipleri kaydedilsin mi.</summary>
    /// <remarks>
    /// Yalnızca tipler; değerler asla kaydedilmez. Parametre değeri kişisel
    /// veri, parola ya da jeton taşıyabilir.
    /// </remarks>
    public bool CaptureParameterTypes { get; set; } = true;
}

/// <summary>
/// DI'a kayıtlı servislerin metotlarını otomatik olarak ölçer.
/// </summary>
/// <remarks>
/// <para>
/// Dynatrace gibi araçlar CLR Profiler API'si ile çalışma anında IL'i yeniden
/// yazar ve hiçbir işaret gerekmeden her metodu görür. Bu paket IL'e
/// dokunmaz: bozuk IL üretmek, izlediği uygulamayı çökerten bir
/// gözlemlenebilirlik aracı demektir.
/// </para>
/// <para>
/// Bunun yerine DI kayıtları sarmalanır. Arayüz üzerinden kayıtlı her servisin
/// her metodu, kod değişikliği olmadan kendi span'ini alır. Karşılığında tek
/// satır gerekir; <c>IHostingStartup</c> ile bunu da kaldırmak denendi ama
/// çalışmıyor: hosting startup, uygulamanın kendi kayıtlarından önce çalışıyor
/// ve sarmalanacak servisleri henüz göremiyor.
/// </para>
/// </remarks>
public static class NabizCodeLevel
{
    internal const string SourceName = "Nabiz.Agent.Services";
    internal static readonly ActivitySource Source = new(SourceName);

    // Çerçeve servislerini sarmalamak hem gürültü hem de risk: bazıları
    // açılış sırasında çağrılır ve proxy'lenmeye uygun değildir.
    private static readonly string[] AlwaysExcluded =
    {
        "System.", "Microsoft.", "OpenTelemetry.", "Nabiz.Agent.",
    };

    /// <summary>
    /// Uygulamanın servislerini otomatik ölçmeye alır.
    /// </summary>
    /// <example>
    /// <code>
    /// var builder = WebApplication.CreateBuilder(args);
    /// builder.Services.AddScoped&lt;ISepetServisi, SepetServisi&gt;();
    /// builder.Services.AddNabizCodeLevel();   // kayıtlardan SONRA
    /// </code>
    /// </example>
    public static IServiceCollection AddNabizCodeLevel(
        this IServiceCollection services, Action<CodeLevelOptions>? configure = null)
    {
        var options = new CodeLevelOptions();
        configure?.Invoke(options);

        if (options.IncludeNamespaces.Count == 0)
        {
            var root = RootNamespace();
            if (root is not null) options.IncludeNamespaces.Add(root);
        }

        var wrapped = 0;
        // Listeyi dolaşırken değiştirdiğimiz için kopya üzerinden gidiyoruz.
        foreach (var descriptor in services.ToList())
        {
            if (!ShouldWrap(descriptor, options)) continue;
            if (Replace(services, descriptor, options)) wrapped++;
        }

        if (NabizAgent.Options?.Debug == true)
        {
            Console.WriteLine($"[nabiz] kod seviyesi: {wrapped} servis sarmalandı " +
                              $"(namespace: {string.Join(", ", options.IncludeNamespaces)})");
        }
        return services;
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
        // DispatchProxy yalnızca arayüzleri sarmalayabilir. Sınıf olarak
        // kayıtlı servisler için sanal metot proxy'si gerekirdi; o da ancak
        // metotlar virtual ise çalışır ve sessizce eksik ölçüm üretir.
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
            // Tek bir servisin sarmalanamaması, uygulamanın açılmamasına yol
            // açmamalı. Atlanır ve devam edilir.
            Console.Error.WriteLine($"[nabiz] {d.ServiceType.Name} sarmalanamadı, atlandı: {ex.Message}");
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
