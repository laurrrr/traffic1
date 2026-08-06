# lantest — test de throughput și latență în LAN

Măsoară cât livrează efectiv rețeaua ta locală între laptop și telefon, și —
mai important — cu cât crește latența când legătura e încărcată. Pe laptop
rulează o aplicație cu fereastră proprie. Pe telefon nu instalezi nimic:
deschizi o adresă în browser, HTTP simplu, fără certificate.

Metrica-vedetă nu e viteza, ci **bufferbloat**: diferența dintre latența în
repaus și latența sub sarcină. Un Wi-Fi care dă 900 Mbps dar urcă de la 3 ms la
400 ms sub încărcare e o rețea pe care apelurile video se blochează.

## Quickstart

Necesită **Go 1.25+**. Cu setările implicite (`GOTOOLCHAIN=auto`) Go descarcă
singur toolchain-ul potrivit, deci merge și de pe o instalare mai veche.

```bash
go build -o lantest .        # binar server-only (fără CGO, fără GUI)
./lantest
```

Serverul afișează adresele LAN și un cod QR. Scanezi cu telefonul, apeși
**Pornește testul**. Ecranul laptopului arată același grafic live.

Pentru fereastra desktop:

```bash
go build -tags desktop -o lantest-desktop .                # macOS, Windows
go build -tags "desktop webkit2_41" -o lantest-desktop .   # Linux
./lantest-desktop
```

### Opțiuni

```
./lantest -port 9090              # alt port (implicit 8080)
./lantest -streams 8              # streamuri paralele implicite: 1, 4 sau 8
./lantest -history /cale/f.json   # alt fișier de istoric
```

## Build pe fiecare platformă

Shell-ul desktop folosește Wails v2, care randează prin webview-ul sistemului.
Asta înseamnă CGO și, implicit, **build nativ pe fiecare platformă** —
cross-compile de pe Linux pentru macOS/Windows nu funcționează.

| Platformă | Dependențe | Comandă |
|-----------|-----------|---------|
| **Linux** | `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` | `go build -tags "desktop webkit2_41" -o lantest-desktop .` |
| **macOS** | Xcode Command Line Tools (WKWebView vine cu sistemul) | `go build -tags desktop -o lantest-desktop .` |
| **Windows** | WebView2 Runtime (preinstalat pe Windows 11) | `go build -tags desktop -o lantest-desktop.exe .` |

Pe Ubuntu/Debian:

```bash
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
```

Build-ul **implicit**, fără tag-uri, nu are nevoie de nimic din toate astea: e
Go pur, fără CGO, potrivit pentru servere și CI. Diferența e doar fereastra.

## Arhitectură

Un singur binar. Frontend-ul e unul singur, embed-uit cu `go:embed`, și rulează
identic în ambele locuri pentru că **nu folosește bindings Wails pentru nimic
din ce se măsoară** — vorbește exclusiv WebSocket cu serverul local:

```
fereastra desktop ──► http.Handler ──► ws://127.0.0.1:8080/ws
telefon (browser) ──► http.Handler ──► ws://192.168.x.x:8080/ws
                       (același)          (același protocol)
```

Bindings-urile Wails există doar pentru ce un tab de browser nu poate face:
dialogul nativ de salvare și deschiderea unui link în browserul sistemului.
Ambele sunt feature-detected, deci lipsa lor nu schimbă nimic pe telefon.

Layout-ul se alege după viewport — „host" (dashboard pe desktop) sau „client"
(un buton mare pe telefon) — cu buton de override manual.

### Conexiuni

O rulare folosește mai multe conexiuni WebSocket cu același ID de sesiune:

- **control** — una singură. Duce PING/PONG pe toată durata rulării, inclusiv
  *în timpul* fazelor de încărcare. Aici nu circulă date în vrac, ceea ce e
  exact motivul pentru care un ping poate trece în timp ce legătura e saturată.
- **stream** — 1, 4 sau 8. Duc doar datele de test.
- **observer** — oricâte, niciodată blocate. Primesc progresul retransmis, ca
  al doilea ecran să deseneze același grafic.

## Ce măsoară, și cum

| Metrică | Metodă |
|---------|--------|
| Latență în repaus | 50 dus-întors pe conexiunea de control; min / p50 / p95 / jitter |
| Download | Serverul trimite 10 s pe N streamuri; **clientul** numără octeții |
| Upload | Clientul trimite 10 s pe N streamuri; **serverul** numără octeții |
| Latență sub sarcină | Ping continuu (100 ms) în timpul download-ului și al upload-ului |
| Bufferbloat | p95 sub sarcină − p50 în repaus, cu notă de la A la F |
| Pierdere de pachete | **NEMĂSURAT** — TCP ascunde retransmisiile |

Fiecare direcție e numărată de capătul care știe adevărul. La download,
`WriteMessage` care întoarce `nil` înseamnă doar că nucleul a acceptat octeții
în bufferul socket-ului, nu că au ajuns la client — deci cifra raportată e a
clientului. La upload e invers. Contorul serverului pentru download se păstrează
ca diagnostic: dacă diferă cu peste 5%, rularea primește un avertisment explicit.

Primele **1000 ms din fiecare direcție se aruncă** înainte de calculul
percentilelor. TCP slow-start plus adaptarea de rată Wi-Fi fac prima secundă
nereprezentativă; incluzând-o, p50 scade și rulările nu mai sunt comparabile.
Fiecare export notează că s-a întâmplat. Se aruncă și ultima fereastră de
eșantionare, pentru că streamurile nu se opresc perfect simultan și ar arăta ca
o prăbușire de throughput care nu a existat.

Se raportează percentile, niciodată medii goale: o singură fereastră blocată
strică o medie.

### Streamuri paralele

Un singur stream TCP rareori saturează Wi-Fi-ul modern. Throughput-ul se agregă
**însumând octeții din aceeași fereastră de timp** pe toate streamurile. Media ar
sub-raporta legătura de N ori; concatenarea ar număra aceeași secundă de mai
multe ori. Ferestrele sunt aliniate la un moment de start comun întregii
sesiuni, ceea ce face suma validă.

### Statistica trăiește într-un singur loc

Frontend-ul nu calculează nimic. Trimite ferestrele brute și timpii de
dus-întors bruți; serverul face tăierea, percentilele și notarea. Așa nu există
o implementare în Go și una în JavaScript care să divergă, iar telefonul,
fereastra desktop și fișierul de istoric nu pot fi în dezacord.

## Istoric și comparație

Fiecare rulare se salvează în directorul de configurare al utilizatorului
(`~/.config/lantest/history.json` pe Linux, echivalentul pe macOS/Windows),
maxim 200 de rulări. Rularea nouă se compară automat cu ultima rulare validă de
pe **aceeași rețea** — identificată prin SSID când se poate citi, altfel prin
subnet. Rulările întrerupte nu sunt folosite ca referință.

## Export

- **JSON** — `schema_version: 1`, rularea completă, rularea anterioară pe aceeași
  rețea, plus o secțiune `method` cu warmup-ul eliminat, lățimea ferestrei și
  care capăt a măsurat fiecare direcție.
- **PNG** — o imagine de sine stătătoare cu verdictul, cifrele și graficul,
  randată pe fundal deschis indiferent de tema aplicației.

În fereastra desktop se deschide dialogul nativ de salvare; în browser se
descarcă normal.

## Limitările reale ale metodei

Astea nu sunt detalii de subsol, sunt motivele pentru care unele cifre nu
înseamnă ce par să spună:

- **Pierderea de pachete nu se poate măsura peste TCP.** TCP retransmite tăcut;
  ce vezi e throughput redus și latență crescută, nu pachete lipsă. Orice unealtă
  care raportează „0% packet loss" peste TCP raportează o tautologie. Aici scrie
  explicit NEMĂSURAT.
- **Se măsoară calea end-to-end, nu doar Wi-Fi-ul.** Dacă laptopul e pe Wi-Fi,
  rezultatul include ambele legături wireless plus router-ul. Pentru a izola
  Wi-Fi-ul telefonului, pune laptopul pe cablu.
- **Browserul e un instrument de măsură imperfect.** Ecranul stins sau tab-ul în
  fundal opresc timerele și blochează socket-ul; rularea e marcată ca nesigură,
  nu raportată ca normală.
- **Throughput-ul de upload live e o estimare.** Clientul poate ști doar câți
  octeți a predat lui `send()` minus ce e încă în coadă. Cifra finală vine de la
  server.
- **Bufferbloat-ul măsurat e al căii de test.** Un ping ICMP către gateway ar
  măsura altceva; aici se măsoară latența traficului care chiar concurează cu
  testul.
- **Un singur test odată.** Al doilea client primește un refuz explicit, nu cifre
  false. Două rulări simultane ar împărți banda și ambele ar greși.
- **Rulările nu se compară între rețele.** De asta istoricul e cheiat pe SSID sau
  subnet.

## Securitate

Serverul se leagă **doar** la adrese private: RFC 1918, link-local, ULA IPv6 și
loopback — câte un listener per adresă, niciodată `0.0.0.0`. Refuză să pornească
dacă nu găsește nicio adresă privată.

WebSocket-ul verifică atât `Host`-ul (trebuie să fie IP privat sau loopback;
numele de host sunt respinse, pentru că un nume poate rezolva oriunde) cât și
`Origin`-ul, când există. Asta e ce împiedică o pagină de pe internet să comande
serverul prin browserul tău. Fereastra desktop e recunoscută separat prin schema
ei de assets.

Nu există TLS și nu e o omisiune: unealta e pentru LAN, iar certificatele ar
însemna avertismente în browserul telefonului pentru zero câștig real.

## Probleme frecvente

### Telefonul nu ajunge la server

**Izolarea clienților (AP/client isolation)** — multe routere de consum și toate
rețelele „guest" blochează traficul între clienți wireless. Telefonul și laptopul
sunt în aceeași rețea, dar pachetele dintre ele sunt aruncate tăcut.

1. Pune laptopul pe cablu și telefonul pe Wi-Fi, sau
2. Dezactivează AP isolation în setările routerului (deseori sub
   Wireless → Advanced), sau
3. Folosește un SSID fără izolare.

### Firewall

```bash
sudo ufw allow 8080/tcp                                   # Linux
sudo iptables -I INPUT -p tcp --dport 8080 -j ACCEPT      # Linux
```

Pe macOS acceptă conexiunile când sistemul întreabă. Pe Windows permite
aplicația în Windows Defender Firewall pentru rețele private.

### „no private LAN addresses found"

Ești pe VPN sau pe o interfață publică. Deconectează VPN-ul sau conectează-te la
o rețea locală.

### Cifrele sunt mult sub așteptări

Încearcă 8 streamuri. Verifică dacă laptopul e pe Wi-Fi în loc de cablu. Deschide
„Avansat" și compară p50 cu p95: o diferență mare înseamnă o legătură instabilă,
nu una lentă.

## Ce nu e implementat

- **Mod WebRTC DataChannel** — ar da pierdere de pachete reală și jitter
  unidirecțional, lucruri pe care TCP nu le poate expune. Protocolul are un seam
  curat pentru asta, documentat în `protocol.go`; nu e implementat.
- **IPv6 link-local** — necesită zone ID (`fe80::1%en0`), pe care browserele nu îl
  acceptă în URL.
- **Mai mulți clienți simultan**, **TLS**, **rulări programate**.

## Dezvoltare

```bash
go vet ./...
go test ./...          # include un ciclu complet împotriva unui server real
go test -race ./...
go test -short ./...   # sare peste testele care mută octeți
```

## Licență

MIT — vezi [LICENSE](LICENSE).
