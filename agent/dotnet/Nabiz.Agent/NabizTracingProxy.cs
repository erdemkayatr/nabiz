using System.Diagnostics;
using System.Linq;
using System.Reflection;

namespace Nabiz.Agent;

/// <summary>
/// Bir servisin her metot çağrısını ölçen saydam sarmalayıcı.
/// </summary>
// sealed DEĞİL: DispatchProxy çalışma anında bu tipten türeyen bir proxy
// üretiyor, mühürlü bir sınıftan türetemez.
internal class NabizTracingProxy : DispatchProxy
{
    private object _target = null!;
    private CodeLevelOptions _options = null!;
    private string _typeName = "";

    // .NET 10, DispatchProxy'ye Create(Type, Type) aşırı yüklemesini ekledi;
    // ada göre arama artık belirsiz. Jenerik olanı parametre imzasıyla
    // ayırıyoruz. Sonuç tip başına önbelleğe alınır: bu kod scoped bir
    // servisin her çözümlemesinde çalışıyor.
    private static readonly MethodInfo CreateMethod =
        typeof(DispatchProxy)
            .GetMethods(BindingFlags.Public | BindingFlags.Static)
            .Single(m => m.Name == nameof(DispatchProxy.Create)
                         && m.IsGenericMethodDefinition
                         && m.GetGenericArguments().Length == 2
                         && m.GetParameters().Length == 0);

    private static readonly System.Collections.Concurrent.ConcurrentDictionary<Type, MethodInfo> CreateCache = new();

    /// <summary>Verilen arayüz için proxy üretir.</summary>
    internal static object Wrap(Type serviceType, object target, CodeLevelOptions options)
    {
        var create = CreateCache.GetOrAdd(serviceType,
            t => CreateMethod.MakeGenericMethod(t, typeof(NabizTracingProxy)));

        var proxy = create.Invoke(null, null)!;
        var self = (NabizTracingProxy)proxy;
        self._target = target;
        self._options = options;
        self._typeName = target.GetType().Name;
        return proxy;
    }

    // Metot başına karar bir kez verilip önbelleğe alınır: her çağrıda
    // attribute taramak, ölçmeye çalıştığımız gecikmeye kendi maliyetimizi
    // eklemek olurdu.
    private readonly System.Collections.Concurrent.ConcurrentDictionary<MethodInfo, string?> _spanNames = new();

    protected override object? Invoke(MethodInfo? method, object?[]? args)
    {
        if (method is null) return null;

        var spanName = _spanNames.GetOrAdd(method, ResolveSpanName);
        if (spanName is null) return InvokeTarget(method, args);   // [NabizIgnore]

        var parent = Activity.Current;
        var activity = NabizCodeLevel.Source.StartActivity(spanName, ActivityKind.Internal);

        // Örnekleme span'i düşürdüyse hiçbir ölçüm yapma: kapalı bir agent'ın
        // maliyeti tek bir null kontrolü olmalı.
        if (activity is null) return InvokeTarget(method, args);

        var allocStart = _options.CaptureAllocations ? GC.GetAllocatedBytesForCurrentThread() : 0;
        var threadStart = System.Environment.CurrentManagedThreadId;

        Annotate(activity, method, args, threadStart);

        try
        {
            var result = InvokeTarget(method, args);

            // Task döndüren metotlarda iş henüz bitmedi; span'i tamamlanınca
            // kapat. Orijinal nesne döndürülür, çağıranın tipi değişmez.
            if (result is Task task)
            {
                // Activity.Current'ı ÇAĞIRANA hemen geri ver.
                //
                // Çağrılan metot ilk await'e kadar senkron koştu ve kendi alt
                // span'lerini bu activity'nin altında açtı; gerisi async
                // akışın kendi ExecutionContext'inde doğru ebeveyni taşıyor.
                // Ama çağıranın bir sonraki satırı da burada başlıyor: Current
                // hâlâ bu activity'yi gösterirse, çağıranın SONRAKİ kardeş
                // çağrısı yanlışlıkla bunun çocuğu olur. O zaman ağaç bozulur
                // ve self time toplamı trace süresini aşar.
                Activity.Current = parent;

                task.ContinueWith(
                    t => Finish(activity, allocStart, threadStart, t.Exception?.InnerException ?? t.Exception),
                    CancellationToken.None,
                    TaskContinuationOptions.ExecuteSynchronously,
                    TaskScheduler.Default);
                return result;
            }

            // ValueTask'i tüketmeden beklemenin yolu yok; AsTask() çağırmak
            // çağıranın elinden sonucu alırdı. Süre yalnızca senkron kısmı
            // kapsar ve span bunu açıkça söyler.
            if (result is ValueTask || (result is not null && IsGenericValueTask(result.GetType())))
            {
                activity.SetTag("nabiz.timing.partial", "value_task_sync_only");
            }

            Finish(activity, allocStart, threadStart, null);
            return result;
        }
        catch (Exception ex)
        {
            Finish(activity, allocStart, threadStart, ex);
            throw;
        }
    }

    /// <summary>
    /// Metodun span adını belirler; ölçülmeyecekse null döner.
    /// </summary>
    private string? ResolveSpanName(MethodInfo method)
    {
        // Hedefteki gerçek metodu bul: attribute'lar çoğunlukla arayüze değil
        // uygulamaya konur.
        var implementation = FindImplementation(method);

        if (HasIgnore(method) || HasIgnore(implementation)) return null;
        if (HasIgnore(method.DeclaringType) || HasIgnore(_target.GetType())) return null;

        var trace = implementation?.GetCustomAttribute<NabizTraceAttribute>()
                    ?? method.GetCustomAttribute<NabizTraceAttribute>();
        if (!string.IsNullOrWhiteSpace(trace?.Name)) return trace!.Name;

        return $"{_typeName}.{method.Name}";
    }

    private MethodInfo? FindImplementation(MethodInfo interfaceMethod)
    {
        try
        {
            var map = _target.GetType().GetInterfaceMap(interfaceMethod.DeclaringType!);
            var index = Array.IndexOf(map.InterfaceMethods, interfaceMethod);
            return index >= 0 ? map.TargetMethods[index] : null;
        }
        catch
        {
            // Açık arayüz uygulamaları ve bazı jenerik durumlar eşlenemeyebilir;
            // bu durumda arayüzdeki attribute'a düşülür.
            return null;
        }
    }

    private static bool HasIgnore(MemberInfo? member) =>
        member?.GetCustomAttribute<NabizIgnoreAttribute>() is not null;

    private object? InvokeTarget(MethodInfo method, object?[]? args)
    {
        try
        {
            return method.Invoke(_target, args);
        }
        catch (TargetInvocationException ex) when (ex.InnerException is not null)
        {
            // Yansıma katmanının sardığı istisnayı çıkar: uygulamanın gördüğü
            // yığın izi proxy yokmuş gibi olmalı.
            System.Runtime.ExceptionServices.ExceptionDispatchInfo
                .Capture(ex.InnerException).Throw();
            throw; // erişilmez
        }
    }

    private void Annotate(Activity activity, MethodInfo method, object?[]? args, int threadId)
    {
        activity.SetTag("code.function.name", method.Name);
        activity.SetTag("code.namespace", _target.GetType().FullName);

        if (_options.CaptureParameterTypes)
        {
            activity.SetTag("code.parameter.count", args?.Length ?? 0);
            var parameters = method.GetParameters();
            if (parameters.Length > 0)
            {
                // Yalnızca tipler. Değerler kişisel veri, parola ya da jeton
                // taşıyabilir; span'e hiç girmezler.
                activity.SetTag("code.parameter.types",
                    string.Join(", ", parameters.Select(p => p.ParameterType.Name)));
            }
        }
        if (_options.CaptureThread) activity.SetTag("thread.id", threadId);
    }

    private void Finish(Activity activity, long allocStart, int threadStart, Exception? error)
    {
        if (_options.CaptureAllocations)
        {
            var delta = GC.GetAllocatedBytesForCurrentThread() - allocStart;
            // Async metotlarda devam farklı bir thread'de koşabilir ve sayaç
            // thread başına tutulur; negatif fark anlamsızdır, atılır.
            if (delta > 0) activity.SetTag("nabiz.allocated.bytes", delta);
        }
        if (_options.CaptureThread)
        {
            var threadEnd = System.Environment.CurrentManagedThreadId;
            if (threadEnd != threadStart)
            {
                activity.SetTag("thread.end.id", threadEnd);
                activity.SetTag("nabiz.async.thread_switched", true);
            }
        }
        if (error is not null)
        {
            activity.SetStatus(ActivityStatusCode.Error, error.Message);
            activity.AddEvent(new ActivityEvent("exception", tags: new ActivityTagsCollection
            {
                { "exception.type", error.GetType().FullName },
                { "exception.message", error.Message },
                { "exception.stacktrace", error.ToString() },
            }));
        }
        // Stop yalnızca Activity.Current == activity ise Current'ı değiştirir.
        // Continuation başka bir akışta koşabildiği için yine de koruyoruz:
        // ölçüm aracı, ölçtüğü akışın bağlamını kirletmemeli.
        var saved = Activity.Current;
        activity.Stop();
        if (!ReferenceEquals(Activity.Current, saved)) Activity.Current = saved;
    }

    private static bool IsGenericValueTask(Type type) =>
        type.IsGenericType && type.GetGenericTypeDefinition() == typeof(ValueTask<>);
}
