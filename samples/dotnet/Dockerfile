# syntax=docker/dockerfile:1

# .NET örnek servisleri. Dikkat: uygulama projelerinde tek bir OpenTelemetry
# NuGet paketi yok. Enstrümantasyon bu Dockerfile'da, çalışma anında ekleniyor.
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
# CLR Profiler API üzerinden IL'i çalışma anında sarar. Uygulama kodu,
# derleme çıktısı ve NuGet grafiği değişmez; bir sonraki sürümde profiler'ı
# kaldırmak tek bir ortam değişkeni meselesi.
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
    # Örnekleme agent tarafında: kuyruk ve ağ maliyeti collector'a hiç ulaşmadan
    # kesilsin. parentbased_traceidratio, bir trace'in tüm servislerde aynı
    # kararı almasını garanti eder.
    OTEL_TRACES_SAMPLER=parentbased_traceidratio \
    OTEL_TRACES_SAMPLER_ARG=1.0 \
    # Batch exporter: istek yolunda ağ çağrısı yok, arka planda toplu gönderim.
    OTEL_BSP_SCHEDULE_DELAY=2000 \
    OTEL_BSP_MAX_QUEUE_SIZE=8192 \
    OTEL_BSP_MAX_EXPORT_BATCH_SIZE=1024 \
    OTEL_EXPORTER_OTLP_PROTOCOL=grpc \
    OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none

EXPOSE 8080

# instrument.sh, profiler ortam değişkenlerini doğru mimari (x64/arm64) için
# ayarlar; elle CORECLR_* yazmaktan daha dayanıklı.
ENTRYPOINT ["/bin/sh", "-c", ". /otel/instrument.sh && exec dotnet /app/$PROJECT_DLL"]
