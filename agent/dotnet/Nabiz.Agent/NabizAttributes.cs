namespace Nabiz.Agent;

/// <summary>
/// The marked method or type is kept out of automatic measurement.
/// </summary>
/// <remarks>
/// <para>
/// When the whole application is under measurement, some methods produce
/// nothing but noise: tiny helpers called inside tight loops, accessors that
/// run hundreds of times per request, or methods where measuring costs more
/// than the work itself. This is where you exclude them.
/// </para>
/// <para>
/// Placed on a type, it excludes every method of that type.
/// </para>
/// </remarks>
/// <example>
/// <code>
/// public class CartService : ICartService
/// {
///     public Task&lt;decimal&gt; TotalAsync(int count) { ... }   // measured
///
///     [NabizIgnore]
///     public bool IsValid(int count) => count > 0;           // not measured
/// }
/// </code>
/// </example>
[AttributeUsage(AttributeTargets.Method | AttributeTargets.Class | AttributeTargets.Interface,
    Inherited = true)]
public sealed class NabizIgnoreAttribute : Attribute
{
}

/// <summary>
/// The marked method or type is explicitly put under measurement.
/// </summary>
/// <remarks>
/// Used when a type falls outside the namespace filter but you still want to
/// watch it. When it appears alongside <see cref="NabizIgnoreAttribute"/>, the
/// exclusion wins: a decision to silence always beats a decision to measure.
/// </remarks>
[AttributeUsage(AttributeTargets.Method | AttributeTargets.Class | AttributeTargets.Interface,
    Inherited = true)]
public sealed class NabizTraceAttribute : Attribute
{
    /// <summary>The span name. When omitted, "Type.Method" is used.</summary>
    public string? Name { get; set; }
}
