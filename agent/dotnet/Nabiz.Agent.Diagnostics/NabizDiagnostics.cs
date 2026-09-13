using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.Diagnostics.NETCore.Client;
using Microsoft.Extensions.DependencyInjection;
using Nabiz.Agent;

namespace Nabiz.Agent.Diagnostics;

/// <summary>
/// Adds the on-demand CPU profile and memory dump endpoints to the application.
/// </summary>
/// <remarks>
/// <para>
/// <b>A memory dump writes the entire memory of the process to disk:</b>
/// connection strings, session tokens, passwords, customer data. That is why
/// the feature is off by default and does not turn on without a token.
/// </para>
/// <para>
/// Setup is one line; everything else is managed from the <c>diagnostics</c>
/// section of nabiz.json:
/// </para>
/// <code>
/// builder.Services.AddNabizDiagnostics();
/// </code>
/// </remarks>
public static class NabizDiagnosticsExtensions
{
    /// <summary>Registers the diagnostics endpoints.</summary>
    public static IServiceCollection AddNabizDiagnostics(
        this IServiceCollection services,
        Action<NabizOptions.DiagnosticsSettings>? configure = null)
    {
        var settings = NabizAgent.Options?.Diagnostics ?? new NabizOptions.DiagnosticsSettings();
        configure?.Invoke(settings);

        if (!settings.Enabled)
        {
            Console.WriteLine("[nabiz] diagnostics endpoints are off (diagnostics.enabled = false)");
            return services;
        }

        // They never open without a token. A configuration left at
        // enabled=true by accident would hand the process's memory to anyone
        // who asked.
        if (string.IsNullOrWhiteSpace(settings.Token) || settings.Token.Length < 16)
        {
            Console.Error.WriteLine(
                "[nabiz] diagnostics endpoints NOT opened: diagnostics.token must be at " +
                "least 16 characters. A memory dump contains connection strings and " +
                "tokens; it cannot be exposed without a token.");
            return services;
        }

        var store = new DumpStore(settings);
        services.AddSingleton(store);
        services.AddSingleton(settings);
        services.AddSingleton<Microsoft.AspNetCore.Hosting.IStartupFilter>(
            new DiagnosticsStartupFilter(settings, store));

        // Register ourselves so this can be triggered from the nabiz UI.
        services.AddHostedService(provider => new AgentRegistration(
            settings, provider.GetRequiredService<Microsoft.AspNetCore.Hosting.Server.IServer>()));

        Console.WriteLine($"[nabiz] diagnostics endpoints open: {settings.Path} " +
                          $"(directory: {store.Directory_}, download: {(settings.AllowDownload ? "on" : "off")})");
        return services;
    }
}

/// <summary>
/// Places the diagnostics middleware at the very front of the pipeline.
/// </summary>
/// <remarks>
/// IStartupFilter is used so the application's <c>Program.cs</c> needs no
/// <c>app.Map...</c> call of its own. The endpoints run before routing: it must
/// stay possible to take a dump while the application's own pipeline is broken.
/// </remarks>
internal sealed class DiagnosticsStartupFilter(
    NabizOptions.DiagnosticsSettings settings, DumpStore store) : IStartupFilter
{
    public Action<IApplicationBuilder> Configure(Action<IApplicationBuilder> next) => app =>
    {
        app.Use(async (context, nextMiddleware) =>
        {
            if (!context.Request.Path.StartsWithSegments(settings.Path, out var remaining))
            {
                await nextMiddleware().ConfigureAwait(false);
                return;
            }
            await DiagnosticsEndpoints.HandleAsync(context, remaining, settings, store).ConfigureAwait(false);
        });
        next(app);
    };
}

/// <summary>The body of the diagnostics endpoints.</summary>
internal static class DiagnosticsEndpoints
{
    private static readonly JsonSerializerOptions Json = new() { WriteIndented = true };

    public static async Task HandleAsync(
        HttpContext context, PathString remaining,
        NabizOptions.DiagnosticsSettings settings, DumpStore store)
    {
        if (!IsAuthorized(context, settings))
        {
            // 401, not 404: whoever holds the token already knows the path
            // exists, and whoever arrives with the wrong one deserves a clear
            // answer.
            context.Response.StatusCode = StatusCodes.Status401Unauthorized;
            await WriteAsync(context, new { error = "X-Nabiz-Token is invalid" }).ConfigureAwait(false);
            return;
        }

        var segment = remaining.Value?.Trim('/') ?? "";
        var method = context.Request.Method;

        try
        {
            switch (method, segment)
            {
                case ("POST", "cpu"):
                {
                    var seconds = ReadInt(context, "seconds", 20);
                    var file = await store.CaptureCpuAsync(seconds, context.RequestAborted).ConfigureAwait(false);
                    Log(context, $"CPU profile, {seconds}s");
                    await WriteAsync(context, Describe(file, settings)).ConfigureAwait(false);
                    return;
                }
                case ("POST", "memory"):
                {
                    var type = ReadDumpType(context);
                    var file = await store.CaptureMemoryAsync(type, context.RequestAborted).ConfigureAwait(false);
                    Log(context, $"memory dump ({type})");
                    await WriteAsync(context, Describe(file, settings)).ConfigureAwait(false);
                    return;
                }
                case ("POST", "cancel"):
                {
                    var kind = store.RunningKind;
                    var stopped = store.Cancel();
                    Log(context, $"stop request ({(kind.Length > 0 ? kind : "idle")}) -> " +
                                 (stopped ? "stopped" : "could not stop"));
                    await WriteAsync(context, new
                    {
                        cancelled = stopped,
                        running = kind,
                        reason = stopped ? null
                            : kind == "memory"
                                ? "a memory dump cannot be interrupted once started: the runtime suspends the process and writes the file"
                                : "there is no operation to stop",
                    }).ConfigureAwait(false);
                    return;
                }
                case ("GET", ""):
                    await WriteAsync(context, new
                    {
                        directory = store.Directory_,
                        downloadEnabled = settings.AllowDownload,
                        files = store.List().Select(f => Describe(f, settings)),
                    }).ConfigureAwait(false);
                    return;

                case ("GET", _) when segment.Length > 0:
                {
                    if (!settings.AllowDownload)
                    {
                        context.Response.StatusCode = StatusCodes.Status403Forbidden;
                        await WriteAsync(context, new
                        {
                            error = "downloading is disabled (diagnostics.allowDownload = false)",
                            directory = store.Directory_,
                        }).ConfigureAwait(false);
                        return;
                    }
                    var file = store.Find(segment);
                    if (file is null) { context.Response.StatusCode = StatusCodes.Status404NotFound; return; }

                    Log(context, $"download {file.Id} ({file.Bytes / 1024 / 1024} MB)");
                    context.Response.ContentType = "application/octet-stream";
                    context.Response.Headers.ContentDisposition = $"attachment; filename=\"{file.Id}\"";
                    await context.Response.SendFileAsync(file.Path, context.RequestAborted).ConfigureAwait(false);
                    return;
                }

                case ("DELETE", _) when segment.Length > 0:
                    context.Response.StatusCode = store.Delete(segment)
                        ? StatusCodes.Status204NoContent
                        : StatusCodes.Status404NotFound;
                    return;

                default:
                    context.Response.StatusCode = StatusCodes.Status404NotFound;
                    await WriteAsync(context, new
                    {
                        error = "unknown endpoint",
                        endpoints = new[]
                        {
                            $"POST {settings.Path}/cpu?seconds=20",
                            $"POST {settings.Path}/memory?type=heap|full|mini|triage",
                            $"POST {settings.Path}/cancel",
                            $"GET {settings.Path}",
                            $"GET {settings.Path}/{{file}}",
                            $"DELETE {settings.Path}/{{file}}",
                        },
                    }).ConfigureAwait(false);
                    return;
            }
        }
        catch (Exception ex)
        {
            // A failure in the diagnostics endpoint must not affect the application.
            context.Response.StatusCode = StatusCodes.Status500InternalServerError;
            await WriteAsync(context, new { error = ex.Message, type = ex.GetType().Name }).ConfigureAwait(false);
        }
    }

    /// <summary>Compares the token in constant time.</summary>
    private static bool IsAuthorized(HttpContext context, NabizOptions.DiagnosticsSettings settings)
    {
        var provided = context.Request.Headers["X-Nabiz-Token"].ToString();
        if (string.IsNullOrEmpty(provided)) return false;

        var a = Encoding.UTF8.GetBytes(provided);
        var b = Encoding.UTF8.GetBytes(settings.Token);
        // FixedTimeEquals wants equal lengths; hashing to a fixed size first
        // keeps the length difference from leaking either.
        return CryptographicOperations.FixedTimeEquals(SHA256.HashData(a), SHA256.HashData(b));
    }

    private static object Describe(DumpFile file, NabizOptions.DiagnosticsSettings settings) => new
    {
        id = file.Id,
        kind = file.Kind,
        bytes = file.Bytes,
        megabytes = Math.Round(file.Bytes / 1024d / 1024d, 1),
        createdAt = file.CreatedAt,
        path = file.Path,
        downloadUrl = settings.AllowDownload ? $"{settings.Path}/{file.Id}" : null,
    };

    private static int ReadInt(HttpContext context, string name, int fallback) =>
        int.TryParse(context.Request.Query[name], out var value) ? value : fallback;

    private static DumpType ReadDumpType(HttpContext context) =>
        context.Request.Query["type"].ToString().ToLowerInvariant() switch
        {
            "full" => DumpType.Full,
            "mini" => DumpType.Normal,
            "triage" => DumpType.Triage,
            _ => DumpType.WithHeap,   // default: includes the heap but is not as large as Full
        };

    // Taking a dump has to leave a trace: who took what, and when.
    private static void Log(HttpContext context, string what) =>
        Console.WriteLine($"[nabiz] diagnostics: {what} — client {context.Connection.RemoteIpAddress}");

    private static Task WriteAsync(HttpContext context, object payload)
    {
        context.Response.ContentType = "application/json; charset=utf-8";
        return context.Response.WriteAsync(JsonSerializer.Serialize(payload, Json));
    }
}
