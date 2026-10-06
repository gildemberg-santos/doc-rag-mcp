package httpapi

// dashboardHTML é servido por Dashboard (GET /dashboard). Busca /status
// (mesma origem) a cada poucos segundos e renderiza sem depender de nada
// externo — sem CDN, sem build step, só o que o navegador já tem.
const dashboardHTML = `<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>doc-rag-mcp · status vivo</title>
<style>
  :root{
    --bg:#f5f4f0; --surface:#fff; --surface-2:#edece6; --border:#dddad2;
    --ink:#171a1f; --ink-2:#4b5059; --ink-3:#7b818c;
    --accent:#1f6f8b; --accent-soft:#e3eef1;
    --good:#2f8f5b; --good-soft:#e4f3ea;
    --crit:#c6414a; --crit-soft:#fbe7e8;
  }
  @media (prefers-color-scheme: dark){
    :root{
      --bg:#10141a; --surface:#171c24; --surface-2:#1e242e; --border:#2a313c;
      --ink:#eef0f3; --ink-2:#aeb4bf; --ink-3:#7b828f;
      --accent:#5bb8d4; --accent-soft:#1b3540;
      --good:#52c285; --good-soft:#163427;
      --crit:#e8646c; --crit-soft:#3a1c1f;
    }
  }
  *{box-sizing:border-box}
  body{background:var(--bg);color:var(--ink);font-family:-apple-system,"Segoe UI",system-ui,sans-serif;
    margin:0;padding:24px 20px 48px;font-size:15px;line-height:1.5}
  .wrap{max-width:1000px;margin:0 auto;display:flex;flex-direction:column;gap:22px}
  .mono{font-family:ui-monospace,Menlo,Consolas,monospace}
  .num{font-variant-numeric:tabular-nums}
  h1{font-size:22px;margin:0}
  .topbar{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px;
    border-bottom:1px solid var(--border);padding-bottom:16px}
  .title-row{display:flex;align-items:center;gap:12px}
  .pill{display:inline-flex;align-items:center;gap:6px;padding:5px 12px 5px 10px;border-radius:100px;
    font-size:13px;font-weight:600;white-space:nowrap}
  .pill .dot{width:8px;height:8px;border-radius:50%;flex:none}
  .pill.good{background:var(--good-soft);color:var(--good)} .pill.good .dot{background:var(--good)}
  .pill.crit{background:var(--crit-soft);color:var(--crit)} .pill.crit .dot{background:var(--crit)}
  .refresh-note{font-size:12.5px;color:var(--ink-3)}
  .tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}
  .tile{background:var(--surface);border:1px solid var(--border);border-radius:14px;padding:16px 18px;
    box-shadow:0 1px 2px rgba(0,0,0,.05);display:flex;flex-direction:column;gap:6px;min-width:0}
  .tile .label{font-size:12px;color:var(--ink-3);font-weight:600;text-transform:uppercase;letter-spacing:.05em}
  .tile .value{font-size:28px;font-weight:700;line-height:1.1}
  .tile .sub{font-size:12.5px;color:var(--ink-2)}
  section{display:flex;flex-direction:column;gap:12px}
  .section-head{display:flex;align-items:baseline;justify-content:space-between;gap:10px;flex-wrap:wrap}
  .section-head h2{font-size:17px;margin:0}
  .panel{background:var(--surface);border:1px solid var(--border);border-radius:14px;padding:18px;
    box-shadow:0 1px 2px rgba(0,0,0,.05)}
  .bar-item{display:grid;grid-template-columns:150px 1fr 70px;align-items:center;gap:10px;margin-bottom:12px}
  .bar-item:last-child{margin-bottom:0}
  .bar-item .name{font-size:13px;color:var(--ink-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .bar-track{background:var(--surface-2);border-radius:6px;height:14px;overflow:hidden}
  .bar-fill{height:100%;border-radius:6px;background:var(--accent);transition:width .4s ease}
  .bar-item .count{font-size:13px;text-align:right;font-weight:600}
  table{width:100%;border-collapse:collapse;font-size:13.5px}
  th{text-align:left;color:var(--ink-3);font-weight:600;font-size:11.5px;text-transform:uppercase;
    letter-spacing:.05em;padding:0 10px 8px 0;border-bottom:1px solid var(--border)}
  td{padding:9px 10px 9px 0;border-bottom:1px solid var(--border)}
  tr:last-child td{border-bottom:none}
  .muted{color:var(--ink-3)}
  footer{font-size:12px;color:var(--ink-3);border-top:1px solid var(--border);padding-top:14px}
</style>
</head>
<body>
<div class="wrap">

  <div class="topbar">
    <div class="title-row">
      <h1>doc-rag-mcp · status vivo</h1>
      <span class="pill good" id="health-pill"><span class="dot"></span><span id="health-text">conectando…</span></span>
    </div>
    <div class="refresh-note">atualiza a cada 5s · última checagem: <span id="checked-at" class="mono">—</span></div>
  </div>

  <div class="tiles" id="tiles">
    <div class="tile"><div class="label">Pontos indexados</div><div class="value num" id="t-points">—</div><div class="sub" id="t-collection">—</div></div>
    <div class="tile"><div class="label">Projetos</div><div class="value num" id="t-projects">—</div><div class="sub">no índice ativo</div></div>
    <div class="tile"><div class="label">Provider</div><div class="value" id="t-provider">—</div><div class="sub" id="t-dims">—</div></div>
    <div class="tile"><div class="label">Conexão</div><div class="value" id="t-latency">—</div><div class="sub">tempo da última chamada</div></div>
  </div>

  <section>
    <div class="section-head"><h2>Projetos</h2></div>
    <div class="panel">
      <div id="bars"></div>
    </div>
  </section>

  <section>
    <div class="section-head"><h2>Detalhe</h2></div>
    <div class="panel">
      <table>
        <tbody>
          <tr><td class="muted" style="width:160px">Projeto</td><td class="muted">Chunks</td><td class="muted">Atualizado</td></tr>
        </tbody>
        <tbody id="proj-table"></tbody>
      </table>
    </div>
  </section>

  <footer>Servido pelo próprio mcp-server em <span class="mono">/dashboard</span> (lê <span class="mono">/status</span>, mesma origem) · doc-rag-mcp</footer>
</div>

<script>
(function(){
  var maxChunks = 1;
  function fmt(n){ return new Intl.NumberFormat('pt-BR').format(n); }
  function timeAgo(iso){
    if(!iso) return '—';
    var d = new Date(iso);
    if(isNaN(d.getTime())) return iso;
    var s = Math.max(0, Math.round((Date.now() - d.getTime())/1000));
    if(s<60) return s+'s atrás';
    if(s<3600) return Math.round(s/60)+'min atrás';
    return Math.round(s/3600)+'h atrás';
  }
  function setHealth(ok, msg){
    var pill = document.getElementById('health-pill');
    pill.className = 'pill ' + (ok ? 'good' : 'crit');
    document.getElementById('health-text').textContent = msg;
  }
  function render(data){
    document.getElementById('t-points').textContent = fmt(data.points || 0);
    document.getElementById('t-collection').textContent = data.collection || '—';
    document.getElementById('t-projects').textContent = (data.projects || []).length;
    document.getElementById('t-provider').textContent = data.provider || '—';
    document.getElementById('t-dims').textContent = (data.dims || '—') + ' dims';
    document.getElementById('checked-at').textContent = new Date(data.checked_at).toLocaleTimeString('pt-BR');

    var projects = (data.projects || []).slice().sort(function(a,b){ return b.chunks - a.chunks; });
    maxChunks = Math.max(1, projects[0] ? projects[0].chunks : 1);

    var bars = document.getElementById('bars');
    bars.innerHTML = '';
    projects.forEach(function(p){
      var pct = Math.max(2, Math.round(100 * p.chunks / maxChunks));
      var row = document.createElement('div');
      row.className = 'bar-item';
      row.innerHTML =
        '<span class="name">' + p.project + '</span>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%"></div></div>' +
        '<span class="count num">' + fmt(p.chunks) + '</span>';
      bars.appendChild(row);
    });

    var tbody = document.getElementById('proj-table');
    tbody.innerHTML = '';
    projects.forEach(function(p){
      var tr = document.createElement('tr');
      tr.innerHTML =
        '<td>' + p.project + '</td>' +
        '<td class="num">' + fmt(p.chunks) + '</td>' +
        '<td class="muted">' + timeAgo(p.updated_at) + '</td>';
      tbody.appendChild(tr);
    });
  }
  function tick(){
    var t0 = performance.now();
    fetch('/status', {cache:'no-store'}).then(function(r){ return r.json().then(function(d){ return {r:r, d:d}; }); })
      .then(function(res){
        var ms = Math.round(performance.now() - t0);
        document.getElementById('t-latency').textContent = ms + 'ms';
        if(!res.r.ok || res.d.ok === false){
          setHealth(false, 'erro: ' + (res.d.error || res.r.status));
          return;
        }
        setHealth(true, 'Qdrant OK');
        render(res.d);
      })
      .catch(function(err){
        setHealth(false, 'sem conexão');
        document.getElementById('t-latency').textContent = '—';
      });
  }
  tick();
  setInterval(tick, 5000);
})();
</script>
</body>
</html>`
