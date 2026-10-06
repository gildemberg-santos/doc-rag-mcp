package httpapi

// dashboardHTML é servido por Dashboard (GET /dashboard). Busca /status
// (mesma origem) a cada poucos segundos e renderiza sem depender de nada
// externo — sem CDN, sem build step, só o que o navegador já tem.
//
// Cobre os 3 níveis do /status:
//
//	N1 (sem acesso novo): saúde da coleção, indexed vs points, coleções
//	  órfãs, dims mismatch, saúde do provider de embedding, self/uso.
//	N2 (arquivo compartilhado): heartbeat do indexer --watch.
//	N3 (socket opt-in + série temporal): containers + histórico.
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
    --warn:#b9791c; --warn-soft:#faf0de;
    --crit:#c6414a; --crit-soft:#fbe7e8;
  }
  @media (prefers-color-scheme: dark){
    :root{
      --bg:#10141a; --surface:#171c24; --surface-2:#1e242e; --border:#2a313c;
      --ink:#eef0f3; --ink-2:#aeb4bf; --ink-3:#7b828f;
      --accent:#5bb8d4; --accent-soft:#1b3540;
      --good:#52c285; --good-soft:#163427;
      --warn:#e0a53f; --warn-soft:#3a2f16;
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
  .title-row{display:flex;align-items:center;gap:12px;flex-wrap:wrap}
  .pill{display:inline-flex;align-items:center;gap:6px;padding:5px 12px 5px 10px;border-radius:100px;
    font-size:13px;font-weight:600;white-space:nowrap}
  .pill .dot{width:8px;height:8px;border-radius:50%;flex:none}
  .pill.good{background:var(--good-soft);color:var(--good)} .pill.good .dot{background:var(--good)}
  .pill.warn{background:var(--warn-soft);color:var(--warn)} .pill.warn .dot{background:var(--warn)}
  .pill.crit{background:var(--crit-soft);color:var(--crit)} .pill.crit .dot{background:var(--crit)}
  .refresh-note{font-size:12.5px;color:var(--ink-3)}
  .tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:12px}
  .tile{background:var(--surface);border:1px solid var(--border);border-radius:14px;padding:16px 18px;
    box-shadow:0 1px 2px rgba(0,0,0,.05);display:flex;flex-direction:column;gap:6px;min-width:0}
  .tile .label{font-size:12px;color:var(--ink-3);font-weight:600;text-transform:uppercase;letter-spacing:.05em}
  .tile .value{font-size:28px;font-weight:700;line-height:1.1}
  .tile .value.small{font-size:20px}
  .tile .sub{font-size:12.5px;color:var(--ink-2)}
  .tile .sub.good{color:var(--good)} .tile .sub.warn{color:var(--warn)} .tile .sub.crit{color:var(--crit)}
  .alerts{display:flex;flex-direction:column;gap:8px}
  .alert{background:var(--surface);border:1px solid var(--border);border-left:4px solid var(--warn);
    border-radius:10px;padding:10px 14px;font-size:13.5px}
  .alert.crit{border-left-color:var(--crit)} .alert.good{border-left-color:var(--good)}
  .alert .tag{font-weight:700;margin-right:6px}
  .alert.crit .tag{color:var(--crit)} .alert .tag{color:var(--warn)} .alert.good .tag{color:var(--good)}
  section{display:flex;flex-direction:column;gap:12px}
  .section-head{display:flex;align-items:baseline;justify-content:space-between;gap:10px;flex-wrap:wrap}
  .section-head h2{font-size:17px;margin:0}
  .section-head .hint{font-size:12.5px;color:var(--ink-3)}
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
  td{padding:9px 10px 9px 0;border-bottom:1px solid var(--border);vertical-align:top}
  tr:last-child td{border-bottom:none}
  .muted{color:var(--ink-3)}
  .good-t{color:var(--good)} .warn-t{color:var(--warn)} .crit-t{color:var(--crit)}
  canvas.spark{width:100%;height:120px;display:block}
  .legend{display:flex;gap:16px;font-size:12px;color:var(--ink-3);margin-top:8px;flex-wrap:wrap}
  .legend i{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:5px}
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

  <div class="alerts" id="alerts"></div>

  <div class="tiles" id="tiles">
    <div class="tile"><div class="label">Pontos indexados</div><div class="value num" id="t-points">—</div><div class="sub" id="t-collection">—</div></div>
    <div class="tile"><div class="label">Projetos</div><div class="value num" id="t-projects">—</div><div class="sub">no índice ativo</div></div>
    <div class="tile"><div class="label">Provider</div><div class="value small" id="t-provider">—</div><div class="sub" id="t-dims">—</div></div>
    <div class="tile"><div class="label">Conexão</div><div class="value small" id="t-latency">—</div><div class="sub">tempo da última chamada</div></div>
    <div class="tile"><div class="label">Uptime</div><div class="value small num" id="t-uptime">—</div><div class="sub" id="t-mem">—</div></div>
    <div class="tile"><div class="label">Uso (MCP)</div><div class="value num" id="t-reqs">—</div><div class="sub" id="t-avglat">—</div></div>
  </div>

  <section>
    <div class="section-head"><h2>Projetos</h2><span class="hint">chunks por projeto no índice ativo</span></div>
    <div class="panel">
      <div id="bars"></div>
    </div>
  </section>

  <section>
    <div class="section-head"><h2>Saúde da coleção</h2><span class="hint">Qdrant GET /collections/{ativa}</span></div>
    <div class="panel"><div id="chealth"><span class="muted">carregando…</span></div></div>
  </section>

  <section>
    <div class="section-head"><h2>Coleções órfãs</h2><span class="hint">existem no Qdrant, fora da config atual</span></div>
    <div class="panel"><div id="orphans"><span class="muted">carregando…</span></div></div>
  </section>

  <section>
    <div class="section-head"><h2>Embeddings</h2><span class="hint">provider alcançável? (ping barato, sem custo)</span></div>
    <div class="panel"><div id="embed"><span class="muted">carregando…</span></div></div>
  </section>

  <section>
    <div class="section-head"><h2>Indexer Watch</h2><span class="hint">heartbeat via arquivo compartilhado</span></div>
    <div class="panel"><div id="indexer"><span class="muted">carregando…</span></div></div>
  </section>

  <section>
    <div class="section-head"><h2>Containers</h2><span class="hint">mesmo projeto compose · opt-in via socket</span></div>
    <div class="panel"><div id="containers"><span class="muted">carregando…</span></div></div>
  </section>

  <section>
    <div class="section-head"><h2>Histórico</h2><span class="hint">últimos pontos de /status/history</span></div>
    <div class="panel">
      <canvas class="spark" id="spark" width="900" height="120"></canvas>
      <div class="legend"><span><i style="background:#1f6f8b"></i>pontos</span><span><i style="background:#2f8f5b"></i>requisições MCP (eixo próprio)</span><span id="hist-note" class="muted"></span></div>
    </div>
  </section>

  <section>
    <div class="section-head"><h2>Detalhe</h2></div>
    <div class="panel">
      <table>
        <thead><tr><th>Projeto</th><th>Chunks</th><th>Atualizado</th></tr></thead>
        <tbody id="proj-table"></tbody>
      </table>
    </div>
  </section>

  <footer>Servido pelo próprio mcp-server em <span class="mono">/dashboard</span> (lê <span class="mono">/status</span>, mesma origem) · doc-rag-mcp</footer>
</div>

<script>
(function(){
  function fmt(n){ return new Intl.NumberFormat('pt-BR').format(n); }
  function esc(s){ return String(s == null ? '' : s).replace(/[&<>"]/g, function(c){ return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }
  function timeAgo(iso){
    if(!iso) return '—';
    var d = new Date(iso);
    if(isNaN(d.getTime())) return esc(iso);
    var s = Math.max(0, Math.round((Date.now() - d.getTime())/1000));
    if(s<60) return s+'s atrás';
    if(s<3600) return Math.round(s/60)+'min atrás';
    if(s<86400) return Math.round(s/3600)+'h atrás';
    return Math.round(s/86400)+'d atrás';
  }
  function fmtUptime(s){
    if(s == null) return '—';
    if(s<60) return s+'s';
    if(s<3600) return Math.floor(s/60)+'min';
    if(s<86400) return Math.floor(s/3600)+'h '+Math.floor(s%3600/60)+'min';
    return Math.floor(s/86400)+'d '+Math.floor(s%86400/3600)+'h';
  }
  function setHealth(kind, msg){
    var pill = document.getElementById('health-pill');
    pill.className = 'pill ' + kind;
    document.getElementById('health-text').textContent = msg;
  }
  function addAlert(kind, tag, html){
    var box = document.getElementById('alerts');
    var div = document.createElement('div');
    div.className = 'alert ' + kind;
    div.innerHTML = '<span class="tag">' + esc(tag) + '</span><span>' + html + '</span>';
    box.appendChild(div);
  }
  function kvTable(rows){
    var h = '<table><tbody>';
    rows.forEach(function(r){ h += '<tr><td class="muted" style="width:190px">' + esc(r[0]) + '</td><td>' + r[1] + '</td></tr>'; });
    return h + '</tbody></table>';
  }

  function render(data){
    document.getElementById('t-points').textContent = fmt(data.points || 0);
    document.getElementById('t-collection').textContent = (data.collection || '—') + ' · ' + (data.qdrant_url || '');
    document.getElementById('t-projects').textContent = (data.projects || []).length;
    document.getElementById('t-provider').textContent = data.provider || '—';
    document.getElementById('t-dims').textContent = (data.dims || '—') + ' dims';
    var self = data.self || {};
    document.getElementById('t-uptime').textContent = fmtUptime(self.uptime_seconds);
    document.getElementById('t-mem').textContent = (self.heap_alloc_mb != null ? self.heap_alloc_mb.toFixed(1) + ' MB heap · ' : '') + (self.goroutines != null ? self.goroutines + ' goroutines' : '');
    document.getElementById('t-reqs').textContent = fmt(self.requests_total || 0);
    document.getElementById('t-avglat').textContent = 'latência média ' + (self.avg_latency_ms != null ? self.avg_latency_ms.toFixed(1) + 'ms' : '—');
    try { document.getElementById('checked-at').textContent = new Date(data.checked_at).toLocaleTimeString('pt-BR'); } catch(e){}

    // ---- alertas ----
    var alerts = document.getElementById('alerts');
    alerts.innerHTML = '';
    var crit = 0, warn = 0;
    if(data.dims_mismatch){
      addAlert('crit', 'DIMS', 'provider fala <b class="mono">' + esc(data.dims) + ' dims</b> mas a coleção tem <b class="mono">' + esc(data.collection_health && data.collection_health.vector_size) + '</b> — reindex com o provider certo.');
      crit++;
    }
    if(data.collection_health){
      var ch = data.collection_health;
      if(ch.status !== 'green' || ch.optimizer_status !== 'ok'){
        addAlert('crit', 'QDRANT', 'coleção <b class="mono">' + esc(ch.status) + '</b> · otimizador <b class="mono">' + esc(ch.optimizer_status) + '</b> — ver logs do Qdrant.');
        crit++;
      }
      var diff = (ch.indexed_vectors_count || 0) - (ch.points_count || 0);
      if(diff > 1000 || (ch.points_count > 0 && diff / ch.points_count > 0.05)){
        addAlert('', 'OTIMIZADOR', esc(fmt(diff)) + ' vetores indexados a mais que pontos (' + esc(fmt(ch.indexed_vectors_count)) + ' vs ' + esc(fmt(ch.points_count)) + ') — pendentes de limpeza pelo otimizador.');
        warn++;
      }
    }
    if(data.embed_health && data.embed_health.checked && !data.embed_health.ok){
      addAlert('crit', 'EMBEDDING', 'provider <b class="mono">' + esc(data.provider) + '</b> inalcançável: ' + esc(data.embed_health.error || 'ping falhou') + ' — buscas vão quebrar.');
      crit++;
    }
    if(data.indexer && data.indexer.fail_count > 0){
      addAlert('crit', 'INDEXER', esc(data.indexer.fail_count) + ' projeto(s) falharam no último ciclo (' + esc(data.indexer.updated_at || '') + ').');
      crit++;
    }
    if(data.containers){
      var down = data.containers.filter(function(c){ return c.state !== 'running'; });
      if(down.length){
        addAlert('', 'DOCKER', esc(down.length) + ' container(s) fora do ar: ' + esc(down.map(function(c){ return c.name; }).join(', ')) + '.');
        warn++;
      }
    }
    if(crit > 0) setHealth('crit', crit + ' problema(s) crítico(s)');
    else if(warn > 0) setHealth('warn', 'Qdrant OK · ' + warn + ' aviso(s)');
    else setHealth('good', 'tudo certo');

    // ---- projetos ----
    var projects = (data.projects || []).slice().sort(function(a,b){ return b.chunks - a.chunks; });
    var maxChunks = Math.max(1, projects[0] ? projects[0].chunks : 1);
    var bars = document.getElementById('bars');
    bars.innerHTML = projects.length ? '' : '<span class="muted">nenhum projeto indexado ainda.</span>';
    projects.forEach(function(p){
      var pct = Math.max(2, Math.round(100 * p.chunks / maxChunks));
      var row = document.createElement('div');
      row.className = 'bar-item';
      row.innerHTML =
        '<span class="name">' + esc(p.project) + '</span>' +
        '<div class="bar-track"><div class="bar-fill" style="width:' + pct + '%"></div></div>' +
        '<span class="count num">' + fmt(p.chunks) + '</span>';
      bars.appendChild(row);
    });
    var tbody = document.getElementById('proj-table');
    tbody.innerHTML = '';
    projects.forEach(function(p){
      var tr = document.createElement('tr');
      tr.innerHTML =
        '<td>' + esc(p.project) + '</td>' +
        '<td class="num">' + fmt(p.chunks) + '</td>' +
        '<td class="muted">' + timeAgo(p.updated_at) + '</td>';
      tbody.appendChild(tr);
    });

    // ---- saúde da coleção ----
    var chEl = document.getElementById('chealth');
    if(data.collection_health){
      var c = data.collection_health;
      var stCls = c.status === 'green' ? 'good-t' : 'crit-t';
      var opCls = c.optimizer_status === 'ok' ? 'good-t' : 'crit-t';
      chEl.innerHTML = kvTable([
        ['Status', '<b class="' + stCls + '">' + esc(c.status) + '</b>'],
        ['Otimizador', '<b class="' + opCls + '">' + esc(c.optimizer_status) + '</b>'],
        ['Pontos', '<span class="num">' + fmt(c.points_count) + '</span>'],
        ['Vetores indexados', '<span class="num">' + fmt(c.indexed_vectors_count) + '</span>'],
        ['Segmentos', '<span class="num">' + esc(c.segments_count) + '</span>'],
        ['Vetor', '<span class="mono">' + esc(c.vector_size) + ' dims · ' + esc(c.distance) + '</span>'],
      ]);
    } else {
      chEl.innerHTML = '<span class="muted">sem dados (Qdrant não respondeu ao GET /collections).</span>';
    }

    // ---- órfãs ----
    var orEl = document.getElementById('orphans');
    if(data.other_collections && data.other_collections.length){
      var h = '<table><thead><tr><th>Coleção</th><th>Pontos</th><th></th></tr></thead><tbody>';
      data.other_collections.forEach(function(o){
        h += '<tr><td class="mono">' + esc(o.name) + '</td><td class="num">' + fmt(o.points) + '</td><td class="muted">fora da config atual</td></tr>';
      });
      orEl.innerHTML = h + '</tbody></table>';
    } else {
      orEl.innerHTML = '<span class="muted good-t">nenhuma — só a coleção ativa existe.</span>';
    }

    // ---- embeddings ----
    var emEl = document.getElementById('embed');
    if(data.embed_health){
      var e = data.embed_health;
      if(!e.checked) emEl.innerHTML = '<span class="muted">provider sem ping implementado — nada a checar.</span>';
      else if(e.ok) emEl.innerHTML = '<span class="good-t"><b>OK</b></span> — <span class="mono">' + esc(data.provider) + '</span> alcançável, modelo certo.';
      else emEl.innerHTML = '<span class="crit-t"><b>FALHA</b></span> — <span class="mono">' + esc(e.error || 'ping falhou') + '</span>';
    } else {
      emEl.innerHTML = '<span class="muted">sem dados.</span>';
    }

    // ---- indexer ----
    var ixEl = document.getElementById('indexer');
    if(data.indexer){
      var ix = data.indexer;
      var rows = [
        ['Último ciclo', esc(timeAgo(ix.updated_at)) + ' <span class="muted mono">' + esc(ix.updated_at || '') + '</span>'],
        ['Duração', '<span class="num">' + esc(ix.cycle_duration_ms) + 'ms</span> a cada ' + esc(ix.interval_seconds) + 's'],
        ['Resultado', '<span class="num">' + esc(ix.ok_count) + ' ok · ' + esc(ix.fail_count) + ' falhas</span> · ' + esc(fmt(ix.total_chunks)) + ' chunks'],
        ['Próximo ciclo', esc(ix.next_cycle_at ? timeAgo(ix.next_cycle_at) : '—') + (ix.next_cycle_at ? ' <span class="muted mono">' + esc(ix.next_cycle_at) + '</span>' : '')],
      ];
      var ph = '';
      if(ix.projects && ix.projects.length){
        ph = '<table style="margin-top:12px"><thead><tr><th>Projeto</th><th>Chunks</th><th>Duração</th><th>Erro</th></tr></thead><tbody>';
        ix.projects.forEach(function(p){
          ph += '<tr><td>' + esc(p.project) + '</td><td class="num">' + fmt(p.chunks) + '</td><td class="num">' + esc(p.duration_ms) + 'ms</td><td class="muted">' + (p.error ? esc(p.error) : '—') + '</td></tr>';
        });
        ph += '</tbody></table>';
      }
      ixEl.innerHTML = kvTable(rows) + ph;
    } else {
      ixEl.innerHTML = '<span class="muted">sem heartbeat — indexer --watch parado ou sem volume compartilhado (INDEXER_STATUS_FILE).</span>';
    }

    // ---- containers ----
    var ctEl = document.getElementById('containers');
    if(data.containers && data.containers.length){
      var ch2 = '<table><thead><tr><th>Container</th><th>Imagem</th><th>Estado</th><th>Status</th></tr></thead><tbody>';
      data.containers.forEach(function(cn){
        var cls = cn.state === 'running' ? 'good-t' : 'crit-t';
        ch2 += '<tr><td class="mono">' + esc(cn.name) + '</td><td class="mono muted">' + esc(cn.image) + '</td><td class="' + cls + '"><b>' + esc(cn.state) + '</b></td><td class="muted">' + esc(cn.status) + '</td></tr>';
      });
      ctEl.innerHTML = ch2 + '</tbody></table>';
    } else {
      ctEl.innerHTML = '<span class="muted">sem dados — socket Docker não montado (opt-in, ver compose) ou nenhum container no projeto.</span>';
    }
  }

  function drawHistory(samples){
    var cv = document.getElementById('spark');
    var ctx = cv.getContext('2d');
    var W = cv.width, H = cv.height;
    ctx.clearRect(0, 0, W, H);
    var note = document.getElementById('hist-note');
    if(!samples || !samples.length){
      note.textContent = 'sem amostras ainda';
      return;
    }
    note.textContent = samples.length + ' amostras';
    var pts = samples.map(function(s){ return s.points || 0; });
    var reqs = samples.map(function(s){ return s.requests_total || 0; });
    function line(vals, color, min, max){
      if(max <= min) max = min + 1;
      ctx.strokeStyle = color; ctx.lineWidth = 2; ctx.beginPath();
      vals.forEach(function(v, i){
        var x = 8 + i * (W - 16) / Math.max(1, vals.length - 1);
        var y = H - 8 - (v - min) / (max - min) * (H - 24);
        if(i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
      });
      ctx.stroke();
    }
    function minmax(a){ var mn = a[0], mx = a[0]; a.forEach(function(v){ if(v<mn) mn=v; if(v>mx) mx=v; }); return [mn, mx]; }
    var pmm = minmax(pts), rmm = minmax(reqs);
    line(pts, '#1f6f8b', pmm[0], pmm[1]);
    line(reqs, '#2f8f5b', rmm[0], rmm[1]);
  }

  function tick(){
    var t0 = performance.now();
    fetch('/status', {cache:'no-store'}).then(function(r){ return r.json().then(function(d){ return {r:r, d:d}; }); })
      .then(function(res){
        var ms = Math.round(performance.now() - t0);
        document.getElementById('t-latency').textContent = ms + 'ms';
        if(!res.r.ok || res.d.ok === false){
          setHealth('crit', 'erro: ' + ((res.d && res.d.error) || res.r.status));
          return;
        }
        render(res.d);
      })
      .catch(function(){
        setHealth('crit', 'sem conexão');
        document.getElementById('t-latency').textContent = '—';
      });
  }
  function tickHistory(){
    fetch('/status/history?n=120', {cache:'no-store'}).then(function(r){ return r.json(); })
      .then(function(d){ drawHistory(d.samples || []); })
      .catch(function(){});
  }
  tick();
  tickHistory();
  setInterval(tick, 5000);
  setInterval(tickHistory, 30000);
})();
</script>
</body>
</html>`
