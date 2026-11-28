package server

import "html/template"

type sharePage struct {
	PublicShare
	Nonce    string
	HasCover bool
}

// sharePageTemplate is a self-contained listening page. Everything inline is
// gated by the per-response CSP nonce; there are no external assets.
var sharePageTemplate = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{.Title}} · 试听</title>
<style nonce="{{.Nonce}}">
:root{color-scheme:dark}
*{box-sizing:border-box}
html,body{margin:0;min-height:100%}
body{
  font-family:"PingFang SC","Hiragino Sans GB","Microsoft YaHei",system-ui,-apple-system,sans-serif;
  background:radial-gradient(1200px 700px at 20% -10%,#26314a 0%,#11141c 55%,#0a0b10 100%);
  color:#eef1f7;display:flex;justify-content:center;padding:clamp(16px,4vw,48px);
}
.card{width:min(880px,100%);background:rgba(20,23,32,.78);border:1px solid rgba(255,255,255,.08);
  border-radius:24px;padding:clamp(20px,4vw,36px);box-shadow:0 24px 80px rgba(0,0,0,.45);
  display:grid;grid-template-columns:minmax(180px,280px) 1fr;gap:clamp(20px,4vw,36px);align-items:start}
.art{position:relative;aspect-ratio:1;border-radius:18px;overflow:hidden;
  background:linear-gradient(150deg,#2b3550,#161a26);display:flex;align-items:center;justify-content:center}
.art img{width:100%;height:100%;object-fit:cover;display:block}
.art .note{font-size:56px;color:rgba(255,255,255,.35)}
.badge{position:absolute;right:10px;bottom:10px;background:rgba(10,12,18,.72);
  border:1px solid rgba(255,255,255,.16);border-radius:999px;padding:4px 10px;font-size:12px;letter-spacing:.04em}
.meta h1{margin:0 0 6px;font-size:clamp(22px,3.4vw,34px);line-height:1.2;word-break:break-word}
.artists{margin:0;color:#a8b1c6;word-break:break-word}
audio{width:100%;margin:18px 0 8px;accent-color:#7aa2ff}
.lyrics{max-height:42vh;overflow-y:auto;scrollbar-width:thin;margin-top:4px}
.lyrics ol{list-style:none;margin:0;padding:8px 0}
.lyrics li{padding:7px 10px;border-radius:10px;cursor:pointer;transition:background .18s,color .18s;
  color:#8b94a9;line-height:1.45}
.lyrics li .text{display:block;font-size:15px}
.lyrics li .translation{display:block;font-size:12px;color:#6c7488;margin-top:2px}
.lyrics li.active{background:rgba(122,162,255,.14);color:#fff}
.lyrics li.active .translation{color:#b8c4e6}
.lyrics .empty{color:#6c7488;text-align:center;padding:24px 0}
.comments{margin-top:18px;border-top:1px solid rgba(255,255,255,.08);padding-top:14px}
.comments h2{font-size:14px;font-weight:600;color:#a8b1c6;margin:0 0 8px}
.comments ul{list-style:none;margin:0;padding:0;display:grid;gap:10px}
.comments li p{margin:0 0 3px;font-size:14px;line-height:1.5;color:#d7dcea;word-break:break-word}
.comments li span{font-size:12px;color:#6c7488}
footer{grid-column:1/-1;display:flex;flex-wrap:wrap;gap:8px 18px;justify-content:space-between;
  border-top:1px solid rgba(255,255,255,.08);padding-top:14px;font-size:12px;color:#6c7488}
footer a{color:#8fb0ff;text-decoration:none}
footer a:hover{text-decoration:underline}
@media(max-width:640px){
  .card{grid-template-columns:1fr}
  .art{width:min(62vw,240px);margin:0 auto}
  .lyrics{max-height:36vh}
}
</style>
</head>
<body>
<main class="card">
  <div class="art">
    {{if .HasCover}}<img src="/s/{{.ID}}/cover" alt="封面">{{else}}<span class="note">♪</span>{{end}}
    {{if eq .Mode "clip"}}<span class="badge">片段</span>{{end}}
  </div>
  <div class="meta">
    <h1>{{.Title}}</h1>
    <p class="artists">{{.Artists}}</p>
    <audio id="audio" controls preload="metadata" src="/s/{{.ID}}/audio"></audio>
    <section class="lyrics" id="lyrics" aria-label="歌词">
      {{if .Lyrics}}
      <ol>
        {{range .Lyrics}}
        <li data-start="{{.AtMs}}" data-end="{{.EndMs}}">
          <span class="text">{{.Text}}</span>
          {{if .Translation}}<span class="translation">{{.Translation}}</span>{{end}}
        </li>
        {{end}}
      </ol>
      {{else}}
      <p class="empty">暂无歌词</p>
      {{end}}
    </section>
    {{if .HotComments}}
    <section class="comments">
      <h2>热门评论</h2>
      <ul>
        {{range .HotComments}}
        <li>
          <p>{{.Text}}</p>
          <span>{{if .Author}}{{.Author}}{{else}}匿名{{end}}{{if .Likes}} · 赞 {{.Likes}}{{end}}</span>
        </li>
        {{end}}
      </ul>
    </section>
    {{end}}
  </div>
  <footer>
    <span>个人分享服务 · 仅供试听</span>
  </footer>
</main>
<script nonce="{{.Nonce}}">
(function () {
  var audio = document.getElementById('audio');
  var list = document.getElementById('lyrics');
  if (!audio || !list) return;
  var lines = Array.prototype.slice.call(list.querySelectorAll('li'));
  if (!lines.length) return;
  var current = -1;
  function activeIndex(ms) {
    var found = -1;
    for (var i = 0; i < lines.length; i++) {
      var start = Number(lines[i].getAttribute('data-start')) || 0;
      var end = Number(lines[i].getAttribute('data-end')) || 0;
      if (start <= ms) found = i;
      if (start <= ms && ms < end) return i;
    }
    return found;
  }
  function update() {
    if (!audio.duration) return;
    var index = activeIndex(audio.currentTime * 1000);
    if (index === current) return;
    if (current >= 0 && lines[current]) lines[current].classList.remove('active');
    current = index;
    if (index < 0) return;
    var line = lines[index];
    line.classList.add('active');
    var view = list.getBoundingClientRect();
    var box = line.getBoundingClientRect();
    if (box.top < view.top || box.bottom > view.bottom) {
      list.scrollTo({ top: line.offsetTop - list.clientHeight / 2 + line.clientHeight / 2, behavior: 'smooth' });
    }
  }
  audio.addEventListener('timeupdate', update);
  audio.addEventListener('seeked', update);
  audio.addEventListener('loadedmetadata', update);
  lines.forEach(function (line) {
    line.addEventListener('click', function () {
      var start = Number(line.getAttribute('data-start')) || 0;
      audio.currentTime = start / 1000;
      audio.play().catch(function () {});
    });
  });
  update();
})();
</script>
</body>
</html>
`))
