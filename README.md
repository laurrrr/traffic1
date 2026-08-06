# lantest — test de throughput și latență în LAN

Măsoară cât livrează efectiv rețeaua ta locală între laptop și telefon, și —
mai important — cu cât crește latența când legătura e încărcată. Pe laptop
rulează o aplicație cu fereastră proprie. Pe telefon nu instalezi nimic:
deschizi o adresă în browser, HTTP simplu, fără certificate.

Traficul e real: serverul trimite octeți dintr-un buffer aleator preîncărcat și
numără ce ajunge la celălalt capăt. Nu sunt cifre simulate.

Metrica-vedetă nu e viteza, ci **bufferbloat**: diferența dintre latența în
repaus și latența sub sarcină. Un Wi-Fi care dă 900 Mbps dar urcă de la 3 ms la
400 ms sub încărcare e o rețea pe care apelurile video se blochează.

---

## Cuprins

1. [Cerințe](#cerințe)
2. [Instalare](#instalare)
3. [Primul test, pas cu pas](#primul-test-pas-cu-pas)
4. [Opțiuni de linie de comandă](#opțiuni-de-linie-de-comandă)
5. [Ce vezi în terminal](#ce-vezi-în-terminal)
6. [Cum se folosește interfața](#cum-se-folosește-interfața)
7. [Arhitectură](#arhitectură)
8. [Metodologia măsurătorii](#metodologia-măsurătorii)
9. [Setările rețelei laptopului](#setările-rețelei-laptopului)
10. [Structura proiectului, fișier cu fișier](#structura-proiectului-fișier-cu-fișier)
11. [Limitările reale ale metodei](#limitările-reale-ale-metodei)
12. [Securitate](#securitate)
13. [Probleme frecvente](#probleme-frecvente)
14. [Dezvoltare](#dezvoltare)

---

## Cerințe

### Obligatoriu

| | Versiune | De ce |
|---|---|---|
| **Go** | 1.25 sau mai nou | Cerut de Wails v2.13 și de `golang.org/x/net`. Cu `GOTOOLCHAIN=auto` (implicit), Go descarcă singur toolchain-ul potrivit, deci merge și de pe o instalare mai veche. |
| **O rețea privată** | RFC 1918 / link-local | Unealta refuză să pornească dacă nu găsește nicio adresă privată. Vezi [Securitate](#securitate). |

Binarul **server-only** (build implicit) nu are nevoie de nimic altceva: e Go
pur, fără CGO, fără biblioteci de sistem.

### Doar pentru fereastra desktop

Shell-ul desktop folosește **Wails v2**, care randează prin webview-ul
sistemului de operare. Asta înseamnă CGO și biblioteci native:

| Platformă | Ce trebuie instalat |
|---|---|
| **Linux** | `libgtk-3-dev` și `libwebkit2gtk-4.1-dev` (sau `-4.0-dev` pe distribuții mai vechi) |
| **macOS** | Xcode Command Line Tools — `xcode-select --install`. WKWebView vine cu sistemul. |
| **Windows** | WebView2 Runtime, preinstalat pe Windows 11 și pe Windows 10 actualizat |

Pe Ubuntu/Debian:

```bash
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
```

### Opțional, la rulare

Detecția setărilor Wi-Fi (SSID, bandă, canal) apelează unelte de sistem. Dacă
lipsesc, aplicația merge normal — doar că panoul „Conexiunea acestui calculator"
va avea mai puține câmpuri.

| Platformă | Unealtă | Pachet |
|---|---|---|
| Linux | `iw` | `iw` |
| Linux | `nmcli` (completează securitatea și semnalul în %) | `network-manager` |
| macOS | `system_profiler`, `networksetup` | incluse în sistem |
| Windows | `netsh` | inclus în sistem |

### Dependențe Go

Trei directe, toate cu licențe permisive:

| Modul | Rol |
|---|---|
| `github.com/gorilla/websocket` | Conexiunile WebSocket ale protocolului de test |
| `github.com/skip2/go-qrcode` | Codul QR — în terminal și ca PNG la `/qr.png` |
| `github.com/wailsapp/wails/v2` | Fereastra desktop. Compilată **doar** cu tag-ul `desktop`; build-ul implicit nu o atinge. |

Frontend-ul are **zero** dependențe: fără npm, fără build step, fără CDN. Tot
HTML/CSS/JS-ul e scris de mână și embed-uit în binar cu `go:embed`.

---

## Instalare

```bash
git clone https://github.com/laurrrr/traffic1.git
cd traffic1
```

### Varianta 1 — server-only (recomandată pentru prima încercare)

Fără CGO, fără GUI, merge oriunde:

```bash
go build -o lantest .
./lantest
```

### Varianta 2 — cu fereastră desktop

**Ambele tag-uri sunt obligatorii:** `desktop` selectează shell-ul din acest
repo, `production` e cerut de Wails. Fără `production`, Wails compilează un stub
care eșuează la rulare — de aceea build-ul e configurat să refuze din start, cu
mesajul corect în eroarea de compilare.

| Platformă | Comandă |
|---|---|
| **Linux** (WebKitGTK 4.1) | `go build -tags "desktop,production,webkit2_41" -o lantest-desktop .` |
| **Linux** (WebKitGTK 4.0) | `go build -tags "desktop,production" -o lantest-desktop .` |
| **macOS** | `go build -tags "desktop,production" -o lantest-desktop .` |
| **Windows** | `go build -tags "desktop,production" -ldflags "-H windowsgui" -o lantest-desktop.exe .` |

Pe Windows, `-H windowsgui` scapă de fereastra de consolă din spatele
aplicației. Pentru binare mai mici, adaugă `-ldflags "-w -s"`.

> **Cross-compile nu funcționează pentru varianta desktop.** Wails are nevoie de
> CGO și de webview-ul nativ, deci fiecare platformă se compilează pe ea însăși.
> Varianta server-only se cross-compilează fără probleme.

---

## Primul test, pas cu pas

1. **Conectează laptopul la rețea.** Ideal pe cablu, dacă vrei să măsori
   Wi-Fi-ul telefonului izolat — altfel rezultatul include ambele legături
   wireless.

2. **Pornește serverul.**

   ```bash
   ./lantest
   ```

   Dacă vezi `no private LAN addresses found`, ești pe VPN sau pe o interfață
   publică. Deconectează VPN-ul.

3. **Citește bannerul.** Îți spune pe ce rețea ești, cum e conectat laptopul,
   unde se salvează istoricul și ce adrese poate folosi telefonul.

4. **Deschide pe telefon.** Scanează codul QR din terminal, sau tastează adresa
   afișată (`http://192.168.x.x:8080`). Telefonul trebuie să fie pe **aceeași
   rețea**. Dacă pagina nu se încarcă, vezi
   [Probleme frecvente](#probleme-frecvente) — cel mai des e izolarea clienților
   pe Wi-Fi.

5. **Alege setările** pe ecranul telefonului: direcție, durată, streamuri
   paralele. Implicit: ambele direcții, 10 secunde, 4 streamuri.

6. **Apasă „Pornește testul".** Ține ecranul aprins — un telefon care se blochează
   oprește timerele browserului, iar rularea e marcată ca nesigură.

7. **Urmărește pe laptop.** Ecranul desktop oglindește același grafic live, iar
   terminalul afișează fiecare stream cu tuplul lui TCP.

8. **Citește rezultatul.** Verdictul într-o frază, apoi cifrele. Detaliile
   tehnice sunt sub „Avansat".

Prima rulare durează ~25 de secunde (1 s latență în repaus + 10 s download +
10 s upload, plus conectare).

---

## Opțiuni de linie de comandă

```
./lantest -port 9090              # alt port (implicit 8080)
./lantest -streams 8              # streamuri paralele implicite: 1, 4 sau 8
./lantest -history /cale/f.json   # alt fișier de istoric
```

Numărul de streamuri dat aici e doar **valoarea implicită** a interfeței;
selectorul din pagină îl suprascrie pentru fiecare rulare.

---

## Ce vezi în terminal

### La pornire

```
  lantest — test de throughput și latență în LAN
  ──────────────────────────────────────────────
  Rețea:    Acasa_5G (192.168.1.0/24)
  Legătură: Wi-Fi Acasa_5G (5 GHz, canal 44/80 MHz, 802.11ax, -47 dBm) · rată radio 867 Mbps  [wlp3s0]
  Istoric:  /home/tu/.config/lantest/history.json

  Deschide pe telefon:

    http://192.168.1.10:8080

  [cod QR]
```

### La începutul unei rulări

```
── 12:46:06 Test pornit ───────────────────────────────────
   Client:    Android / Chrome (192.168.1.23)
   Sesiune:   06a286be04ed21fa
   Mod:       automat, 10 s pe direcție · download și upload · 4 streamuri · cadre de 64 KiB
   Streamuri:
     #0  192.168.1.23:35244 → 192.168.1.10:8080
     #1  192.168.1.23:35258 → 192.168.1.10:8080
     #2  192.168.1.23:35274 → 192.168.1.10:8080
     #3  192.168.1.23:35278 → 192.168.1.10:8080
```

Tuplurile TCP sunt acolo ca să poți confirma din `ss -tn` sau Wireshark că sunt
chiar N conexiuni separate, și pe ce porturi sursă.

### La finalul fiecărei direcții

```
── 12:46:16 Download încheiat (contorul serverului) ───────
     #    Octeți       Cadre      Mbps        Cotă   Blocaje
     0    154 MiB      2 456      128        25.2%         0
     1    152 MiB      2 436      127        25.0%         0
     2    153 MiB      2 444      127        25.0%         1
     3    152 MiB      2 426      126        24.9%         0
     tot  610 MiB      9 762      507                      1
```

**Coloana „Cotă" e motivul pentru care există tabelul.** Patru streamuri la 25%
fiecare și unul la 90% cu trei la 3% dau exact același total, dar numai primul e
o legătură sănătoasă. Când repartiția e vizibil strâmbă, tabelul o spune în text
și numește streamul.

„Blocaje" numără scrierile care au durat peste 200 ms — semn că celălalt capăt a
încetat să golească socket-ul.

Cifrele din tabel sunt ale **serverului**. La download cifra raportată în
rezultat rămâne a clientului; vezi
[Metodologia măsurătorii](#metodologia-măsurătorii).

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

## Setările rețelei laptopului

Ecranul de start arată cum e conectat calculatorul pe care rulează serverul:
cablu sau Wi-Fi, iar dacă e Wi-Fi — SSID, bandă, canal, lățime de canal,
standard 802.11, semnal, securitate, BSSID și rata radio negociată. Aceleași
date se salvează cu fiecare rulare și apar sub „Avansat", pentru că **același
SSID pe 2.4 GHz și pe 5 GHz sunt rețele diferite** din punct de vedere al
debitului, iar o rulare fără contextul ăsta nu se poate interpreta mai târziu.

Detecția e best effort și diferă per sistem:

| Sistem | Unealtă | Ce obține |
|--------|---------|-----------|
| **Linux** | `iw dev <if> link`, completat cu `nmcli` | SSID, BSSID, frecvență, semnal în dBm, rate, lățime; securitate din nmcli |
| **macOS** | `system_profiler SPAirPortDataType` | SSID, standard PHY, canal + bandă + lățime, securitate, semnal/zgomot, rată |
| **Windows** | `netsh wlan show interfaces` | SSID, BSSID, tip radio, bandă, canal, rate, semnal în procente |
| Cablu, Linux | `/sys/class/net/<if>/speed` și `/duplex` | viteza portului și duplex |
| Cablu, macOS | `networksetup -getmedia` | la fel |

Ce nu se poate citi lipsește pur și simplu; nimic de aici nu poate opri
aplicația. Banda și canalul se derivă din frecvență când unealta dă doar
frecvența — cele trei benzi își numerotează canalele de la ancore diferite, așa
că nu e o singură formulă.

Două lucruri de reținut:

- **Rata radio nu e o măsurătoare.** E viteza negociată a legăturii, adică
  plafonul teoretic. Testul măsoară ce livrează efectiv rețeaua, și e normal să
  fie considerabil mai mică.
- **Sondarea nu atinge niciodată un test.** Se face la pornire și apoi în fundal
  la 30 s, iar rezultatul e păstrat în cache; `system_profiler` poate dura
  secunde, și nici încărcarea paginii nici finalul unui test nu au voie să
  aștepte după el.

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

### Grafic interactiv

Graficul nu e doar o imagine. Trage peste el ca să mărești un interval, folosește
rotița ca să mărești în jurul cursorului, dublu-clic (sau dublu-tap) ca să revii
la toată rularea. Cursorul afișează valorile exacte din punctul respectiv:
momentul, throughput-ul și latența.

Zoom-ul nu e doar vizual — **scările verticale și decimarea se recalculează din
fereastra vizibilă**. Într-o rulare de câteva minute, graficul complet e redus la
900 de puncte (păstrând vârful fiecărui interval); dacă mărești pe 5 secunde,
vezi eșantioanele individuale, marcate cu puncte. Asta e diferența dintre a
mări o imagine și a rezolva detaliu.

Pe telefon, tragerea orizontală aparține graficului, iar cea verticală derulează
pagina în continuare.

Zoom-ul schimbă **doar imaginea**. Cifrele din carduri și din verdict se
calculează întotdeauna pe toată rularea — de asta scrie asta lângă grafic când e
mărit. Exportul PNG păstrează intervalul afișat și notează în subsol care e.

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
