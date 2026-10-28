// Package testsite supplies deterministic, local-only interaction fixtures.
// Production node launch/configuration does not import or enable this adapter.
package testsite

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

type Stats struct {
	PointerX, PointerY         float64
	Moves, Clicks, DragMoves   int
	Value, Selected, Key, Code string
	Modifiers                  int
	ScrollX, ScrollY           float64
	Verified                   bool
	ViewportW, ViewportH       int
	Zoom                       float64
}
type Site struct {
	mu       sync.Mutex
	latest   Stats
	requests int
}

func (s *Site) Snapshot() Stats { s.mu.Lock(); defer s.mu.Unlock(); return s.latest }
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/links" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, Links)
		return
	}
	if r.URL.Path == "/link-redirect" {
		http.Redirect(w, r, "http://fixture.test/destination?redirected=1", 302)
		return
	}
	if r.URL.Path == "/destination" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<!doctype html><title>Destination</title><style>body{background:#15b055;color:white;font:24px sans-serif}</style><h1>New Wolf tab destination</h1>")
		return
	}

	if r.URL.Path == "/events" {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var next Stats
		if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&next) != nil {
			w.WriteHeader(400)
			return
		}
		s.mu.Lock()
		s.latest = next
		s.requests++
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/redirect" {
		http.Redirect(w, r, "http://fixture.test/interaction", 302)
		return
	}
	if r.URL.Path == "/denied" {
		w.WriteHeader(403)
		io.WriteString(w, "<h1>Controlled access-denied response</h1>")
		return
	}
	if r.URL.Path == "/limited" {
		w.WriteHeader(429)
		io.WriteString(w, "<h1>Controlled rate-limit response</h1>")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, Page)
}

const Page = `<!doctype html><html><head><meta charset="utf-8"><title>WolfTunnel local interaction lab</title>
<style>
*{box-sizing:border-box}body{margin:0;background:#1555b0;color:#fff;font:16px system-ui;min-width:1500px;min-height:2200px}
button,input{font:16px system-ui;border:0;border-radius:6px;padding:10px}button{cursor:pointer;background:#e0f8ef;color:#103a30}input{background:white;color:#132335}
#click{position:absolute;left:20px;top:20px;width:180px;height:70px}#text{position:absolute;left:20px;top:120px;width:240px;height:40px}
.card{position:absolute;background:#10283e;border:1px solid #577a98;border-radius:12px;padding:18px;box-shadow:0 8px 25px #0016}h2{font-size:18px;margin:0 0 12px}small{color:#cbddeb}
#verification{left:300px;top:20px;width:430px;height:180px}#choice{width:22px;height:22px;vertical-align:middle}#verification p{position:absolute;left:18px;top:68px;margin:0}#answer{position:absolute;left:18px;top:120px;width:150px}#verify{position:absolute;left:180px;top:120px}
#drag{left:20px;top:205px;width:240px;height:90px;user-select:none;cursor:grab;background:#243e50}#select{left:300px;top:220px;width:430px;height:65px}
#scroll{left:20px;top:330px;width:260px;height:110px;overflow:auto}#inside{width:900px;height:700px;background:linear-gradient(140deg,#356565,#24404e)}
#facts{left:300px;top:325px;width:430px;height:185px}#unicode{left:20px;top:480px;width:250px;height:110px}
#tick{font-variant-numeric:tabular-nums}#selection{user-select:text;white-space:nowrap}#pointer{position:fixed;right:12px;bottom:12px;background:#10283e;padding:8px;border-radius:8px}
</style></head><body>
<button id="click">Click interaction</button><input id="text" aria-label="Unicode and keyboard input" placeholder="Type here · Tab navigates">
<section id="verification" class="card"><h2>Local verification fixture</h2><small id="challenge">Awaiting your choice · user interaction only</small><p><label><input id="choice" type="checkbox"> I chose to continue</label></p><input id="answer" aria-label="Verification word" placeholder="Enter WOLF"><button id="verify">Verify</button></section>
<section id="drag" class="card"><h2>Drag / hold pointer</h2><span id="drag-count">0 drag updates</span></section>
<section id="select" class="card"><span id="selection">Select these words with a drag</span></section>
<section id="scroll" class="card"><div id="inside">Two-dimensional fractional scroll region</div></section>
<section id="unicode" class="card">Unicode examples<br>Привет · Ελληνικά<br>café · 日本語 · ✓</section>
<section id="facts" class="card"><h2>Live browser state</h2><div id="view"></div><div id="key">Press a modifier / navigation key</div><div id="value"></div><div>JavaScript tick <span id="tick">0</span></div><small>Cookies and storage belong to this disposable session.</small></section><div id="pointer">Pointer ready</div>
<script>
const el=id=>document.getElementById(id);let stats={PointerX:0,PointerY:0,Moves:0,Clicks:0,DragMoves:0,Value:'',Selected:'',Key:'',Code:'',Modifiers:0,ScrollX:0,ScrollY:0,Verified:false,ViewportW:innerWidth,ViewportH:innerHeight,Zoom:1};let down=false;
function report(){stats.ViewportW=innerWidth;stats.ViewportH=innerHeight;stats.Zoom=visualViewport.scale;el('view').textContent=innerWidth+' × '+innerHeight+' · zoom '+stats.Zoom.toFixed(2);fetch('/events',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(stats)}).catch(()=>{});}
el('click').onclick=()=>{stats.Clicks++;document.body.style.background='#15b055';el('click').textContent='Clicked ✓';report()};
document.onmousemove=e=>{stats.PointerX=e.clientX;stats.PointerY=e.clientY;stats.Moves++;el('pointer').textContent='Pointer '+Math.round(e.clientX)+', '+Math.round(e.clientY);if(down){stats.DragMoves++;el('drag-count').textContent=stats.DragMoves+' drag updates'};report()};
el('drag').onmousedown=()=>{down=true};document.onmouseup=()=>{down=false;report()};
el('text').oninput=()=>{stats.Value=el('text').value;el('value').textContent=stats.Value;report()};
document.onselectionchange=()=>{stats.Selected=getSelection().toString();report()};
document.onkeydown=e=>{stats.Key=e.key;stats.Code=e.code;stats.Modifiers=(e.altKey?1:0)|(e.ctrlKey?2:0)|(e.metaKey?4:0)|(e.shiftKey?8:0);el('key').textContent=e.code+' · modifiers '+stats.Modifiers;report()};
el('scroll').onscroll=()=>{stats.ScrollX=el('scroll').scrollLeft;stats.ScrollY=el('scroll').scrollTop;report()};
el('verify').onclick=()=>{stats.Verified=el('choice').checked&&el('answer').value==='WOLF';el('challenge').textContent=stats.Verified?'Verified locally ✓':'Needs your choice and the word WOLF';report()};
addEventListener('resize',report);visualViewport.addEventListener('resize',report);let ticks=0;setInterval(()=>{el('tick').textContent=++ticks},150);report();
</script></body></html>`

// Links exercises normal, target, scripted, redirected and rejected navigation.
const Links = `<!doctype html><meta charset="utf-8"><title>Local link navigation</title><style>body{margin:0;background:#1555b0;color:white;font:18px sans-serif}a,button{position:absolute;left:20px;padding:8px;color:#dffff0}button{color:#163141}#normal{top:20px}#blank{top:80px}#redirect{top:140px}#popup{top:200px}#private{top:260px}#invalid{top:320px}#nested{top:380px}#external{top:440px}p{position:absolute;top:500px}</style><a id="normal" href="/destination?case=normal">Normal destination</a><a id="blank" href="/destination?case=blank" target="_blank">Target blank</a><a id="redirect" href="/link-redirect">Redirect destination</a><button id="popup" onclick="window.open('/destination?case=script','_blank')">User-gesture window.open</button><a id="private" href="http://127.0.0.1/secret">Rejected private link</a><a id="invalid" href="javascript:void(0)">Unsupported link</a><a id="nested" href="/destination?case=nested"><span>Nested link element</span></a><a id="external" href="http://second.fixture.test/destination?case=external">Controlled external host</a><p>Ctrl+click opens a Wolf tab. Non-link area is intentionally empty.</p>`
