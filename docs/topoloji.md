# Topoloji nasıl çıkarılıyor

Bu belge, `internal/topology` paketindeki kararların gerekçesini anlatır.

## Sorun

Bir servis haritası çizmek için "A servisi B servisini çağırıyor" bilgisine
ihtiyaç var. Bu bilgi hiçbir tek span'de yazmaz:

- Çağıran serviste bir `CLIENT` span'i vardır ve hedefin **adını** bilmez —
  yalnızca `http://backend:8080` gibi bir adres bilir.
- Çağrılan serviste bir `SERVER` span'i vardır ve çağıranın **adını** bilmez.

İkisini birleştiren tek şey, `SERVER` span'inin `parent_span_id` alanının
`CLIENT` span'inin `span_id` alanına eşit olmasıdır.

## Çözüm: iki nesilli eşleştirme tablosu

Gelen her `CLIENT` ve `SERVER` span'i, `span_id`'ye göre parçalanmış
(sharded) bir tabloya yazılır. Eşi zaten oradaysa kenar üretilir ve iki kayıt
da silinir.

Eş hiç gelmeyebilir: hedef enstrümante değildir, ya da hedef zaten bir
veritabanıdır. Bu kayıtların süresiz birikmemesi gerekir.

Klasik çözüm her kayda son kullanma zamanı yazıp periyodik olarak taramaktır;
bu, tablo büyüdükçe pahalılaşır. Bunun yerine her shard'da **iki nesil** map
tutulur:

```
lookup  : önce cur'a, sonra prev'e bak
rotasyon: prev'i at, cur'u prev yap, yeni cur aç   (TTL/2'de bir)
```

Silme maliyeti O(1), bellek üst sınırı serbest, kayıtlar TTL/2 ile TTL
arasında yaşar. Atılan nesildeki eşleşmemiş `CLIENT` span'leri kaybedilmez:
hedef adı span attribute'larından türetilip dış bağımlılık kenarına çevrilir.

## Hedef adının türetilmesi

Sıra, en açıklayıcı isimden en genele:

| Öncelik | Kaynak | Örnek sonuç |
|---|---|---|
| 1 | `db.system` + `db.namespace` | `postgresql:orders` |
| 2 | `messaging.system` + hedef | `rabbitmq:siparis-kuyrugu` |
| 3 | `peer.service` | `odeme-servisi` |
| 4 | `rpc.service` | `Shop.Orders.V1` |
| 5 | `server.address` + port | `api.stripe.com:443` |

## Toplama

Kenarlar ham olarak yazılmaz. Dakika kovalarında, kenar kimliği başına
toplanır: çağrı sayısı, hata sayısı, süre toplamı, süre maksimumu ve 14
kovalı bir gecikme histogramı.

Yerel testte 2605 span, 27 kenar satırına indi. Yazma hacmi istek hacmiyle
değil, topolojinin karmaşıklığıyla büyür — bir APM'in ölçeklenebilmesi için
gereken şey tam olarak budur.

## Kenar kimliğinde ne var

```go
type EdgeKey struct {
	Client, Server                   string
	ClientNamespace, ClientWorkload  string
	ServerNamespace, ServerWorkload  string
	ClientNode, ServerNode           string
	ConnType                         ConnType
}
```

Kubernetes boyutları kenarın üstünde taşındığı için aynı veriden üç farklı
grafik çizilebilir: servis, deployment ve namespace seviyesi. Node boyutu
varsayılan olarak kapalıdır; açmak node'lar arası trafiği görünür kılar ama
kenar kardinalitesini node sayısı kadar çarpar.

## Neden gecikme sunucu tarafından alınıyor

Eşleşmiş bir çiftte iki süre vardır: istemcinin gördüğü ve sunucunun
harcadığı. İstemcininki ağ gecikmesini ve bağlantı havuzu beklemesini de
içerir. Servis grafiğinde aranan "B servisi ne kadar yavaş" olduğu için
sunucu tarafı kullanılır; istemci tarafı yalnızca sunucu span'i yoksa devreye
girer.
