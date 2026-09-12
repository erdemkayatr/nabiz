using System.Net.Http.Json;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using Microsoft.Extensions.Hosting;
using Nabiz.Agent;

namespace Nabiz.Agent.Diagnostics;

/// <summary>
/// Uygulamayı düzenli aralıklarla nabiz'e tanıtır.
/// </summary>
/// <remarks>
/// <para>
/// nabiz arayüzünün "hangi örnekler ayakta" sorusunu cevaplayabilmesi ve
/// dump tetikleyebilmesi için bu kayda ihtiyacı var. Telemetriden servis adını
/// biliyoruz ama tanılama ucunun portunu bilmiyoruz.
/// </para>
/// <para>
/// Kayıtta tanılama jetonu da gönderilir. nabiz bunu projeye tanımlı jetonla
/// karşılaştırır; tutmazsa örnek listelenir ama dump tetiklenemez. Bu olmasaydı
/// sahte bir kayıt, nabiz'i jetonu saldırganın adresine göndermeye ikna
/// edebilirdi.
/// </para>
/// </remarks>
internal sealed class AgentRegistration(
    NabizOptions.DiagnosticsSettings settings, IServer server) : BackgroundService
{
    private static readonly TimeSpan Interval = TimeSpan.FromSeconds(60);
    private readonly HttpClient _client = new() { Timeout = TimeSpan.FromSeconds(10) };

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var apiUrl = NabizAgent.Options?.ApiUrl;
        if (string.IsNullOrWhiteSpace(apiUrl))
        {
            Console.WriteLine("[nabiz] apiUrl tanımlı değil; örnek nabiz arayüzünde listelenmeyecek");
            return;
        }

        var url = apiUrl.TrimEnd('/') + "/api/v1/agents/register";

        // Uygulama portunu bağlandıktan sonra okuyabiliriz; BackgroundService
        // sunucu ayağa kalktıktan sonra çalıştığı için burası doğru an.
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await _client.PostAsJsonAsync(url, BuildPayload(), stoppingToken).ConfigureAwait(false);
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                // nabiz erişilemez olabilir; kayıt yapılamaması uygulamayı
                // etkilememeli, bir sonraki turda yeniden denenir.
                if (NabizAgent.Options?.Debug == true)
                {
                    Console.WriteLine($"[nabiz] kayıt gönderilemedi: {ex.Message}");
                }
            }

            try { await Task.Delay(Interval, stoppingToken).ConfigureAwait(false); }
            catch (OperationCanceledException) { return; }
        }
    }

    private object BuildPayload() => new
    {
        serviceName = NabizAgent.Options?.ServiceName is { Length: > 0 } name
            ? name
            : System.Reflection.Assembly.GetEntryAssembly()?.GetName().Name ?? "unknown_service",
        instanceId = InstanceId(),
        hostname = System.Environment.MachineName,
        pod = System.Environment.GetEnvironmentVariable("NABIZ_K8S_POD") ?? "",
        @namespace = System.Environment.GetEnvironmentVariable("NABIZ_K8S_NAMESPACE") ?? "",
        pid = System.Environment.ProcessId,
        agentVersion = typeof(AgentRegistration).Assembly.GetName().Version?.ToString() ?? "",
        advertisedHost = settings.AdvertisedHost,
        diagPort = ResolvePort(),
        diagPath = settings.Path,
        diagReady = settings.Enabled && settings.AllowDownload,
        token = settings.Token,
    };

    // Pod adı varsa onu kullan: bir pod yeniden başladığında yeni bir örnek
    // olarak görünmeli, aynı makinedeki iki süreç de karışmamalı.
    private static string InstanceId()
    {
        var pod = System.Environment.GetEnvironmentVariable("NABIZ_K8S_POD");
        return string.IsNullOrEmpty(pod)
            ? $"{System.Environment.MachineName}:{System.Environment.ProcessId}"
            : pod;
    }

    private int ResolvePort()
    {
        if (settings.AdvertisedPort > 0) return settings.AdvertisedPort;

        var addresses = server.Features.Get<IServerAddressesFeature>()?.Addresses;
        foreach (var address in addresses ?? Array.Empty<string>())
        {
            if (Uri.TryCreate(address, UriKind.Absolute, out var uri) && uri.Port > 0) return uri.Port;
        }
        return 0;
    }
}
