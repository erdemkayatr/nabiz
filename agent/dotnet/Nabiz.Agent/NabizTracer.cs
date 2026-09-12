using System.Diagnostics;
using System.Runtime.CompilerServices;

namespace Nabiz.Agent;

/// <summary>
/// Metot seviyesinde zamanlama için ince bir sarmalayıcı.
/// </summary>
/// <remarks>
/// Otomatik enstrümantasyon istekleri, HTTP çağrılarını ve veritabanı
/// sorgularını görür ama aradaki kendi kodunuzu görmez. Bir isteğin 200 ms
/// sürdüğünü bilmek, o 200 ms'in nerede geçtiğini söylemez.
///
/// Gerçek metot seviyesi profilleme CLR Profiler API'si ile IL'i yeniden
/// yazmayı gerektirir; bu da her metoda ölçüm maliyeti bindirir. Buradaki
/// yaklaşım bilinçli olarak seçmeli: ölçmek istediğiniz yeri siz
/// işaretlersiniz, dosya ve satır bilgisi derleyiciden bedavaya gelir.
/// </remarks>
public static class NabizTracer
{
    /// <summary>Bu sarmalayıcının kullandığı ActivitySource adı.</summary>
    public const string SourceName = "Nabiz.Agent.CodeLevel";

    private static readonly ActivitySource Source = new(SourceName);

    /// <summary>
    /// Bir kod bloğunu ölçer. Dönen nesne <c>using</c> ile kapatılmalıdır.
    /// </summary>
    /// <param name="name">
    /// Span adı. Verilmezse çağıran metodun adı kullanılır.
    /// </param>
    /// <param name="member">Derleyici doldurur; çağıran metodun adı.</param>
    /// <param name="file">Derleyici doldurur; çağıran dosyanın yolu.</param>
    /// <param name="line">Derleyici doldurur; çağrının satır numarası.</param>
    /// <example>
    /// <code>
    /// using var span = NabizTracer.Start();          // metot adıyla
    /// using var span = NabizTracer.Start("fiyat hesapla");
    /// </code>
    /// </example>
    public static NabizSpan Start(
        string? name = null,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        var activity = Source.StartActivity(name ?? member, ActivityKind.Internal);
        if (activity is null) return default;

        // OpenTelemetry code semantic conventions. nabiz arayüzü bu alanları
        // okuyup "hangi dosyanın kaçıncı satırı" bilgisini gösterir.
        activity.SetTag("code.function.name", member);
        if (!string.IsNullOrEmpty(file))
        {
            activity.SetTag("code.file.path", file);
            activity.SetTag("code.line.number", line);
        }
        return new NabizSpan(activity);
    }

    /// <summary>Bir eylemi ölçer.</summary>
    public static void Measure(
        string name, Action action,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            action();
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }

    /// <summary>Bir fonksiyonu ölçer ve sonucunu döndürür.</summary>
    public static T Measure<T>(
        string name, Func<T> func,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            return func();
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }

    /// <summary>Bir asenkron işlemi ölçer.</summary>
    public static async Task<T> MeasureAsync<T>(
        string name, Func<Task<T>> func,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            return await func().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }

    /// <summary>Bir asenkron eylemi ölçer.</summary>
    public static async Task MeasureAsync(
        string name, Func<Task> func,
        [CallerMemberName] string member = "",
        [CallerFilePath] string file = "",
        [CallerLineNumber] int line = 0)
    {
        using var span = Start(name, member, file, line);
        try
        {
            await func().ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            span.Fail(ex);
            throw;
        }
    }
}

/// <summary>
/// <see cref="NabizTracer.Start"/> tarafından döndürülen ölçüm kapsamı.
/// </summary>
/// <remarks>
/// Struct olması bilinçli: örnekleme kararı span'i düşürdüğünde
/// <c>default</c> döner ve hiçbir nesne ayrılmaz. Kapalı bir agent'ın
/// maliyeti bir null kontrolüne iner.
/// </remarks>
public readonly struct NabizSpan : IDisposable
{
    private readonly Activity? _activity;

    internal NabizSpan(Activity? activity) => _activity = activity;

    /// <summary>Altındaki Activity; span örneklenmediyse null.</summary>
    public Activity? Activity => _activity;

    /// <summary>Span'e etiket ekler.</summary>
    public NabizSpan SetTag(string key, object? value)
    {
        _activity?.SetTag(key, value);
        return this;
    }

    /// <summary>Span'i hatalı işaretler ve istisnayı yığın iziyle kaydeder.</summary>
    public NabizSpan Fail(Exception ex)
    {
        if (_activity is null) return this;
        _activity.SetStatus(ActivityStatusCode.Error, ex.Message);
        _activity.AddEvent(new ActivityEvent("exception", tags: new ActivityTagsCollection
        {
            { "exception.type", ex.GetType().FullName },
            { "exception.message", ex.Message },
            { "exception.stacktrace", ex.ToString() },
        }));
        return this;
    }

    /// <inheritdoc />
    public void Dispose() => _activity?.Dispose();
}
