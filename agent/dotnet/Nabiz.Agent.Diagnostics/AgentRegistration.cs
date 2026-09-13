using System.Net.Http.Json;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using Microsoft.Extensions.Hosting;
using Nabiz.Agent;

namespace Nabiz.Agent.Diagnostics;

/// <summary>
/// Registers the application with nabiz at regular intervals.
/// </summary>
/// <remarks>
/// <para>
/// The nabiz UI needs this registration to answer "which instances are up" and
/// to trigger dumps. We know the service name from telemetry, but not the port
/// of the diagnostics endpoint.
/// </para>
/// <para>
/// The registration also carries the diagnostics token. nabiz compares it
/// against the token set on the project; if they do not match, the instance is
/// listed but dumps cannot be triggered. Without that, a forged registration
/// could talk nabiz into sending the token to an attacker's address.
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
            Console.WriteLine("[nabiz] apiUrl is not set; this instance will not appear in the nabiz UI");
            return;
        }

        var url = apiUrl.TrimEnd('/') + "/api/v1/agents/register";

        // The application's port can only be read once it has bound;
        // BackgroundService runs after the server is up, so this is the right
        // moment.
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await _client.PostAsJsonAsync(url, BuildPayload(), stoppingToken).ConfigureAwait(false);
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                // nabiz may be unreachable; a failed registration must not
                // affect the application, and it is retried next round.
                if (NabizAgent.Options?.Debug == true)
                {
                    Console.WriteLine($"[nabiz] could not send the registration: {ex.Message}");
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

    // Use the pod name when there is one: a restarted pod should appear as a
    // new instance, and two processes on the same machine must not collide.
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
