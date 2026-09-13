# syntax=docker/dockerfile:1

# .NET sample services. Note: the application projects carry no OpenTelemetry
# NuGet package at all. Instrumentation is added here, at runtime.
ARG DOTNET_VERSION=10.0

FROM mcr.microsoft.com/dotnet/sdk:${DOTNET_VERSION} AS build
WORKDIR /src
ARG PROJECT
COPY ${PROJECT}/ ./${PROJECT}/
RUN dotnet publish ./${PROJECT}/${PROJECT}.csproj -c Release -o /app

FROM mcr.microsoft.com/dotnet/aspnet:${DOTNET_VERSION} AS runtime
ARG PROJECT
ARG OTEL_AUTO_VERSION=1.16.0

# --- OpenTelemetry .NET auto-instrumentation ---
# wraps IL at runtime through the CLR Profiler API. Application code, build
# output and the NuGet graph are unchanged; removing the profiler in a later
# release is a matter of one environment variable.
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl unzip ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN curl -sSfL -o /tmp/otel-dotnet-auto-install.sh \
        "https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/v${OTEL_AUTO_VERSION}/otel-dotnet-auto-install.sh" \
    && OTEL_DOTNET_AUTO_HOME=/otel VERSION="v${OTEL_AUTO_VERSION}" sh /tmp/otel-dotnet-auto-install.sh \
    && rm /tmp/otel-dotnet-auto-install.sh

WORKDIR /app
COPY --from=build /app .

ENV PROJECT_DLL=${PROJECT}.dll \
    OTEL_DOTNET_AUTO_HOME=/otel \
    ASPNETCORE_URLS=http://+:8080 \
        # Sampling happens in the agent, so queue and network cost are cut before
        # they ever reach the collector. parentbased_traceidratio guarantees a
        # trace gets the same decision in every service.
    OTEL_TRACES_SAMPLER=parentbased_traceidratio \
    OTEL_TRACES_SAMPLER_ARG=1.0 \
        # Batch exporter: no network call on the request path, batched in the background.
    OTEL_BSP_SCHEDULE_DELAY=2000 \
    OTEL_BSP_MAX_QUEUE_SIZE=8192 \
    OTEL_BSP_MAX_EXPORT_BATCH_SIZE=1024 \
    OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
    OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none

EXPOSE 8080

# instrument.sh sets the profiler environment variables for the right
# architecture (x64/arm64); more robust than writing CORECLR_* by hand.
ENTRYPOINT ["/bin/sh", "-c", ". /otel/instrument.sh && exec dotnet /app/$PROJECT_DLL"]
