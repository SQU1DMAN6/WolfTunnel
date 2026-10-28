package browser

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

func TestRepresentativeCaptureWorkloads(t *testing.T) {
	cfg := integrationConfig(t)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/never" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><style>body{margin:0;background:#1555b0;color:white;height:6000px;font:22px sans-serif}#box{position:absolute;top:40px;left:400px;width:240px;height:160px;background:#32ddaa}input{position:fixed;left:20px;top:120px}@keyframes movement{from{transform:translateX(0)}to{transform:translateX(400px)}}</style><input id="input"><div id="box"></div><div id="tick"></div><canvas width="1600" height="900" style="margin-top:500px"></canvas><script>const mode=new URLSearchParams(location.search).get('mode');let n=0;if(mode==='updates')setInterval(()=>document.getElementById('tick').textContent=++n,30);if(mode==='animation')document.getElementById('box').style.animation='movement 1s infinite alternate';if(mode==='images'){let c=document.querySelector('canvas'),x=c.getContext('2d');for(let y=0;y<900;y+=8){x.fillStyle='hsl('+y+',65%,50%)';x.fillRect(0,y,1600,8)}}if(mode==='long_poll'){let i=new Image;i.src='/never';document.body.append(i)}document.title=mode</script>`)
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	proxy, e := browsernet.New(ctx, browsernet.Options{Resolver: testResolver{}, Dial: func(c context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, n, fixture.Listener.Addr().String())
	}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := Start(ctx, cfg, proxy)
	if e != nil {
		proxy.Close()
		t.Fatal(e)
	}
	defer b.Close()
	id := v3.NewID().String()
	if e = b.NewPage(ctx, id); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, id, 1920, 1080); e != nil {
		t.Fatal(e)
	}
	type measurement struct {
		Mode                                           string
		Samples                                        int
		P50MS, P95MS, MaxMS                            float64
		JPEGBytes                                      int
		MemoryBefore, MemoryAfter, CPUBefore, CPUAfter uint64
	}
	var result []measurement
	for _, mode := range []string{"static", "updates", "animation", "images", "scroll_input_resize", "long_poll"} {
		if e = b.Navigate(ctx, id, "http://fixture.test/workload?mode="+mode); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(3 * time.Second)
		for evaluate(t, b, ctx, id, "(document.readyState==='complete'||document.readyState==='interactive').toString()") != "true" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		before, e := b.Resources(ctx)
		if e != nil {
			t.Fatal(e)
		}
		samples := []float64{}
		total := 0
		for n := 0; n < 12; n++ {
			if mode == "scroll_input_resize" {
				for _, input := range []v3.Input{{Kind: "move", X: float64(40 + n*5), Y: 100}, {Kind: "wheel", X: 300, Y: 400, DeltaX: 1.25, DeltaY: 40.5}} {
					if e = b.Input(ctx, input); e != nil {
						t.Fatal(e)
					}
				}
				if n%4 == 0 {
					if e = b.Select(ctx, id, 1920-n*4, 1080); e != nil {
						t.Fatal(e)
					}
				}
			}
			at := time.Now()
			data, _, _, e := b.Capture(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if len(data) == 0 {
				t.Fatal("missing independent capture")
			}
			samples = append(samples, float64(time.Since(at).Microseconds())/1000)
			total += len(data)
		}
		after, e := b.Resources(ctx)
		if e != nil {
			t.Fatal(e)
		}
		sort.Float64s(samples)
		result = append(result, measurement{mode, 12, samples[6], samples[11], samples[11], total, before.Memory, after.Memory, before.CPUTimeNS, after.CPUTimeNS})
	}
	if dir := os.Getenv("WOLF_ARTIFACT_DIR"); dir != "" {
		os.MkdirAll(dir, 0700)
		raw, _ := json.MarshalIndent(result, "", "  ")
		if e = os.WriteFile(filepath.Join(dir, "representative-workloads.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
