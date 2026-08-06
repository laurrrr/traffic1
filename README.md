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
go build -tags "desktop,production" -o lantest-desktop .                # macOS, Windows
go build -tags "desktop,production,webkit2_41" -o lantest-desktop .     # Linux
./lantest-desktop
```

Ambele tag-uri sunt obligatorii: `desktop` selectează shell-ul din acest repo,
`production` e cerut de Wails. Fără `production`, Wails compilează un stub care
eșuează la rulare — build-ul e configurat să refuze din start, cu mesajul
corect.

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
| **Linux** | `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` | `go build -tags "desktop,production,webkit2_41" -o lantest-desktop .` |
| **macOS** | Xcode Command Line Tools (WKWebView vine cu sistemul) | `go build -tags "desktop,production" -o lantest-desktop .` |
| **Windows** | WebView2 Runtime (preinstalat pe Windows 11) | `go build -tags "desktop,production" -ldflags "-H windowsgui" -o lantest-desktop.exe .` |

Pe Linux, `webkit2_41` e necesar pentru WebKitGTK 4.1 (Ubuntu 24.04 și mai nou).
Pe distribuții cu WebKitGTK 4.0 lasă tag-ul deoparte și instalează
`libwebkit2gtk-4.0-dev`. Pe Windows, `-H windowsgui` scapă de fereastra de
consolă din spatele aplicației. Pentru binare mai mici, adaugă `-ldflags "-w -s"`.

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

## Moduri de rulare

| | Automat | Manual |
|---|---------|--------|
| Durată | 10 s pe direcție | până apeși **Oprește** |
| Direcție | ambele, sau doar una | o singură direcție |
| Istoric | comparabil între rulări | comparat doar cu alte rulări manuale de aceeași direcție |

Modul automat e cel calibrat: durată fixă, deci două rulări se pot compara
direct. Modul manual e pentru când vrei să vezi ce face legătura pe termen lung
— saturezi Wi-Fi-ul cât ai nevoie, te plimbi prin casă, și oprești când ai văzut
destul. Graficul acoperă toată rularea, indiferent cât a durat.

Rularea manuală merge într-o singură direcție: „ambele" ar cere două opriri
separate, ceea ce e un control confuz.

O rulare compară doar cu rulări de aceeași formă — același mod, aceeași
direcție, aceeași rețea. Un download manual de 4 minute și o rulare automată de
10 s în ambele sensuri nu măsoară același lucru.

## Ce măsoară, și cum

| Metrică | Metodă |
|---------|--------|
| Latență în repaus | 50 dus-întors pe conexiunea de control; min / p50 / p95 / jitter |
| Download | Serverul trimite 10 s pe N streamuri; **clientul** numără octeții |
| Upload | Clientul trimite 10 s pe N streamuri; **serverul** numără octeții |
| Latență sub sarcină | Ping continuu (100 ms) în timpul download-ului și al upload-ului |
| Bufferbloat | p95 sub sarcină − p50 în repaus, cu notă de la A la F |
| Verificări de încredere | blocaj al firului principal, ping-uri fără răspuns, tampon implicat imposibil |
| Viteză min / max | extremele ferestrelor de 250 ms, după eliminarea warmup-ului |
| Cadre și dimensiune | mesaje WebSocket numărate de capătul care le primește |
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

### „Cadre", nu pachete

Se raportează numărul de **mesaje WebSocket** și dimensiunea încărcăturii utile
a unuia (64 KiB implicit), nu pachete IP. TCP re-segmentează după MTU-ul căii —
un cadru de 64 KiB devine în jur de 45 de segmente pe o cale obișnuită, iar dacă
e activ TSO/GSO nici măcar nucleul nu vede aceeași împărțire ca firul. Numărul
real de pachete de pe fir nu poate fi observat dintr-un browser, așa că nu e
raportat: ar fi o cifră inventată.

Cadrele de download sunt numărate de client, cele de upload de server — același
principiu ca la octeți: numără capătul care știe ce a ajuns.

### Streamuri paralele

Un singur stream TCP rareori saturează Wi-Fi-ul modern. Throughput-ul se agregă
**însumând octeții din aceeași fereastră de timp** pe toate streamurile. Media ar
sub-raporta legătura de N ori; concatenarea ar număra aceeași secundă de mai
multe ori. Ferestrele sunt aliniate la un moment de start comun întregii
sesiuni, ceea ce face suma validă.

### Când o cifră e refuzată

Nota de bufferbloat e dată doar dacă poate fi atribuită rețelei. Verificările se
aplică **doar notelor care acuză** (creștere peste 30 ms, adică nota C sau mai
rea): la A sau B concluzia e „legătura e în regulă sub sarcină", și asta rămâne
adevărat chiar dacă o parte din milisecunde au venit de la browser. Trei condiții
invalidează o notă, fiecare măsurată separat:

1. **Firul principal al browserului a fost blocat** — un timer de 100 ms care
   întârzie mult înseamnă că pagina și-a măsurat propria întârziere.
2. **Ping-urile nu s-au întors** — dacă sub jumătate primesc răspuns, cele care
   ajung sunt prin construcție coada cea mai lentă.
3. **Tamponul implicat e imposibil** — `întârziere × debit` dă câți octeți ar fi
   trebuit să stea în coadă undeva pe drum. Peste 256 MB, coada nu e în rețea.

Când vreuna se declanșează, rularea e marcată nesigură, insigna arată `?` în loc
de o notă, iar verdictul spune ce s-a întâmplat de fapt.

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
- **Pe legături foarte rapide, dispozitivul devine el însuși bufferul.** Un
  browser livrează toate mesajele WebSocket pe un singur fir. Peste câțiva Gbps
  (bucla locală, 2,5/10 GbE) răspunsul la un ping ajunge să stea în coadă în
  spatele datelor de test, iar round-trip-ul măsurat include acea coadă. Firul
  principal poate arăta perfect sănătos în tot acest timp — timerele se declanșează
  la vreme, toate ping-urile primesc răspuns — deci nicio verificare din client nu
  vede problema. Ce o prinde e fizica: întârzierea de așteptare înseamnă tampon
  împărțit la rată, așa că unealta calculează cât tampon ar fi implicat de
  creșterea măsurată. Peste 256 MB refuză să dea o notă și spune de ce, în loc să
  raporteze „bufferbloat foarte sever" pentru ceva ce e de fapt coada propriului
  browser. La viteze de Wi-Fi asta nu se întâmplă niciodată.
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
