namespace Nabiz.Agent;

/// <summary>
/// İşaretli metot ya da tip, otomatik ölçümün dışında tutulur.
/// </summary>
/// <remarks>
/// <para>
/// Tüm uygulamayı ölçüme aldığınızda bazı metotlar gürültüden başka bir şey
/// üretmez: sıkı döngü içinde çağrılan minik yardımcılar, her istekte yüzlerce
/// kez koşan erişimciler, ya da ölçüm maliyetinin işin kendisinden büyük
/// olduğu metotlar. Bunları burada dışlarsınız.
/// </para>
/// <para>
/// Tip üzerine konursa o tipin bütün metotları dışlanır.
/// </para>
/// </remarks>
/// <example>
/// <code>
/// public class SepetServisi : ISepetServisi
/// {
///     public Task&lt;decimal&gt; ToplamAsync(int adet) { ... }   // ölçülür
///
///     [NabizIgnore]
///     public bool GecerliMi(int adet) => adet > 0;            // ölçülmez
/// }
/// </code>
/// </example>
[AttributeUsage(AttributeTargets.Method | AttributeTargets.Class | AttributeTargets.Interface,
    Inherited = true)]
public sealed class NabizIgnoreAttribute : Attribute
{
}

/// <summary>
/// İşaretli metot ya da tip, açıkça ölçüme alınır.
/// </summary>
/// <remarks>
/// Namespace filtresi dışında kalan ama izlemek istediğiniz bir tip varsa
/// kullanılır. <see cref="NabizIgnoreAttribute"/> ile birlikte bulunursa
/// dışlama kazanır: susturma kararı her zaman ölçme kararını yener.
/// </remarks>
[AttributeUsage(AttributeTargets.Method | AttributeTargets.Class | AttributeTargets.Interface,
    Inherited = true)]
public sealed class NabizTraceAttribute : Attribute
{
    /// <summary>Span adı. Verilmezse "Tip.Metot" kullanılır.</summary>
    public string? Name { get; set; }
}
