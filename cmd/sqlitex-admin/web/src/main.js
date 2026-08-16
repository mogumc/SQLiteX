import './style.css'

let cursors = []
let nextCursor = ''
let prefix = ''
let schema = null // {source, tables:[...]}

const fmtBytes = (n) =>
  n == null ? '-' : n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB'

const esc = (s) =>
  String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

// ---- 统计卡片 ----

async function loadStats() {
  const r = await (await fetch('/api/stats')).json()
  document.getElementById('dir').textContent = r.dir + ' · 只读'
  const cards = [
    ['磁盘占用', fmtBytes(r.disk_usage_bytes)],
    ['活跃数据', fmtBytes(r.live_bytes)],
    ['WAL', fmtBytes(r.wal_size_bytes)],
    ['MemTable', fmtBytes(r.memtable_bytes)],
    ['SSTable 数', r.sstable_count],
  ]
  document.getElementById('stats').innerHTML = cards
    .map((c) => '<div class="card"><div class="k">' + c[0] + '</div><div class="v">' + c[1] + '</div></div>')
    .join('')
  const ok = await fetch('/schema').then((x) => x.ok).catch(() => false)
  if (ok) document.getElementById('schemaBtn').style.display = 'inline-block'
}

// ---- Schema 导入与表结构展示 ----

async function loadSchema() {
  const r = await (await fetch('/api/schema')).json()
  schema = r.tables && r.tables.length ? r : null
  renderSchema()
}

async function importSchema(file) {
  const msg = document.getElementById('schemaMsg')
  msg.textContent = '解析中…'
  const fd = new FormData()
  fd.append('file', file, file.name)
  const resp = await fetch('/api/schema', { method: 'POST', body: fd })
  const r = await resp.json()
  if (!resp.ok) {
    msg.textContent = ''
    alert('Schema 解析失败：\n' + (r.error || resp.statusText))
    return
  }
  msg.textContent = '已导入 ' + r.source + '（' + r.tables.length + ' 张表）'
  schema = r
  renderSchema()
}

async function clearSchema() {
  await fetch('/api/schema', { method: 'DELETE' })
  schema = null
  document.getElementById('schemaMsg').textContent = ''
  document.getElementById('protoFile').value = ''
  renderSchema()
}

function renderSchema() {
  const panel = document.getElementById('schemaPanel')
  const sel = document.getElementById('tableSel')
  document.getElementById('clearSchemaBtn').style.display = schema ? 'inline-block' : 'none'
  if (!schema) {
    panel.innerHTML = ''
    sel.innerHTML = '<option value="">全库（原始 key）</option>'
    return
  }
  // 表结构卡片
  panel.innerHTML = schema.tables
    .map((t) => {
      const badges =
        '<span class="badge">TableID ' + t.table_id + '</span>' +
        '<span class="badge">PK: ' + esc(t.primary_key.name) + ' (' + esc(t.primary_key.type) + ')</span>' +
        (t.has_ttl ? '<span class="badge warn">TTL ' + esc(t.ttl || '') + '</span>' : '') +
        '<span class="badge">' + t.fields.length + ' 字段</span>'
      const rows = t.fields
        .map(
          (f) =>
            '<tr><td>' + esc(f.name) + (f.primary ? ' <span class="pk">PK</span>' : '') + '</td><td>' + esc(f.type) +
            '</td><td>' + (f.index !== 'none' ? '<span class="badge ' + (f.index === 'unique' ? 'ok' : '') + '">' + f.index + '</span>' : '-') +
            '</td><td>' + (f.compress ? 'zstd' : '-') + '</td><td>' + (f.repeated ? 'yes' : '-') + '</td></tr>',
        )
        .join('')
      return (
        '<div class="schema-table"><div class="schema-head"><b>' + esc(t.message) + '</b> ' + badges + '</div>' +
        '<table class="fields"><thead><tr><th>字段</th><th>类型</th><th>索引</th><th>压缩</th><th>repeated</th></tr></thead><tbody>' +
        rows + '</tbody></table></div>'
      )
    })
    .join('')
  // 表选择器
  sel.innerHTML =
    '<option value="">全库（原始 key）</option>' +
    schema.tables.map((t) => '<option value="' + t.table_id + '">' + esc(t.message) + '</option>').join('')
}

// ---- Key 浏览（schema 感知：选表 = 标准表格视图，全库 = 原始字节视图） ----

// 当前选中表对象（null = 全库原始模式）
function currentTable() {
  if (!schema) return null
  const id = document.getElementById('tableSel').value
  return schema.tables.find((t) => String(t.table_id) === id) || null
}

function loadKeys() {
  prefix = document.getElementById('prefix').value
  cursors = ['']
  fetchPage('')
}

async function fetchPage(cur) {
  const tbl = currentTable()
  const u = new URL('/api/keys', location)
  if (tbl) {
    u.searchParams.set('table_id', tbl.table_id)
    u.searchParams.set('decode', '1')
  } else {
    u.searchParams.set('prefix', prefix)
  }
  if (cur) u.searchParams.set('cursor', cur)
  const r = await (await fetch(u)).json()
  if (tbl) renderGrid(tbl, r)
  else renderRaw(r)
  nextCursor = r.next_cursor || ''
  document.getElementById('next').style.visibility = nextCursor ? 'visible' : 'hidden'
  document.getElementById('prev').style.visibility = cursors.length > 1 ? 'visible' : 'hidden'
  document.getElementById('pageinfo').textContent =
    (r.entries ? r.entries.length : 0) + ' 条 / 页' +
    (r.total ? ' · 共 ' + r.total + ' 条' + (r.scan_truncated ? '+（超上限截断）' : '') : '')
}

// 标准表格视图：列 = PK + schema 字段（+ TTL），行 = 字段级解码值
function renderGrid(tbl, r) {
  const head = document.getElementById('rowsHead')
  const tb = document.getElementById('rows')
  const cols = ['<th style="width:90px">' + esc(tbl.primary_key.name) + '<span class="thtype"> ' + esc(tbl.primary_key.type) + '</span></th>']
    .concat(
      tbl.fields
        .filter((f) => !f.primary)
        .map((f) => '<th>' + esc(f.name) + '<span class="thtype"> ' + esc(f.type) + '</span></th>'),
    )
  if (tbl.has_ttl) cols.push('<th style="width:150px">TTL</th>')
  head.innerHTML = '<tr>' + cols.join('') + '</tr>'

  if (!r.entries || !r.entries.length) {
    tb.innerHTML = '<tr><td colspan="' + cols.length + '" class="hint">无数据</td></tr>'
    return
  }
  tb.innerHTML = r.entries
    .map((e) => {
      const cells = []
      const d = e.decoded || {}
      cells.push('<td class="pkcell">' + esc(fmtVal(d.pk)) + '</td>')
      const byName = {}
      for (const f of (e.row && e.row.fields) || []) byName[f.name] = f
      for (const f of tbl.fields.filter((x) => !x.primary)) {
        const v = byName[f.name]
        cells.push('<td class="cell" title="' + esc(v ? v.value : '') + '">' +
          (v ? esc(cellVal(v)) : '<span class="hint">—</span>') + '</td>')
      }
      if (tbl.has_ttl) {
        const row = e.row || {}
        cells.push('<td>' + (row.expires_at
          ? esc(row.expires_at.replace('T', ' ').split('.')[0]) + ' ' +
            (row.expired ? '<span class="badge warn">已过期</span>' : '<span class="badge ok">存活</span>')
          : '<span class="hint">—</span>') + '</td>')
      }
      return '<tr class="row" onclick="showDetail(\'' + e.key_b64 + '\')">' + cells.join('') + '</tr>'
    })
    .join('')
}

// 字段单元格：截断值显示省略标记
function cellVal(f) {
  return f.value.length > 64 ? f.value.slice(0, 64) + '…' : f.value
}

// 原始字节视图（全库 / 未导入 schema）
function renderRaw(r) {
  document.getElementById('rowsHead').innerHTML =
    '<tr><th style="width:150px">结构</th><th>Key</th><th style="width:80px">Size</th><th>Value 预览</th></tr>'
  const tb = document.getElementById('rows')
  if (!r.entries || !r.entries.length) {
    tb.innerHTML = '<tr><td colspan="4" class="hint">无数据</td></tr>'
    return
  }
  tb.innerHTML = r.entries
    .map(
      (e) =>
        '<tr class="row" onclick="showDetail(\'' +
        e.key_b64 +
        '\')"><td>' +
        structCell(e.decoded) +
        '</td><td>' +
        esc(e.key) +
        '</td><td class="num">' +
        e.size +
        '</td><td class="hint">' +
        esc(e.preview) +
        '</td></tr>',
    )
    .join('')
}

// 表选择切换时同步前缀输入框的可用性（表模式不使用前缀）
function syncModeUI() {
  document.getElementById('prefix').disabled = !!currentTable()
}

// 结构列：data → 表名+PK；index → 表名+索引字段；无 schema → —
function structCell(d) {
  if (!d) return '<span class="hint">—</span>'
  if (d.kind === 'data') {
    return '<span class="badge ok">行</span> ' + esc(d.table) + '<br><span class="hint">PK ' + esc(fmtVal(d.pk)) + '</span>'
  }
  if (d.kind === 'index') {
    return '<span class="badge">索引</span> ' + esc(d.table || '?') +
      (d.index_field ? '<br><span class="hint">' + esc(d.index_field) + ' = ' + esc(fmtVal(d.index_value)) + '</span>' : '')
  }
  return '<span class="hint">未知</span>'
}

const fmtVal = (v) => (v == null ? '' : typeof v === 'string' && v.length > 48 ? v.slice(0, 48) + '…' : String(v))

function nextPage() {
  if (!nextCursor) return
  cursors.push(nextCursor)
  fetchPage(nextCursor)
}

function prevPage() {
  if (cursors.length <= 1) return
  cursors.pop()
  fetchPage(cursors[cursors.length - 1])
}

// ---- 记录详情：优先 schema 语义化字段，保底十六进制 ----

async function showDetail(b64) {
  const r = await (await fetch('/api/key?k=' + b64)).json()
  const box = document.getElementById('detail')
  box.style.display = 'block'
  const title = '记录详情 · ' + (r.key_print || r.key_hex) + ' · ' + r.size + ' 字节'

  let semantic = ''
  if (r.decoded_value) {
    const dv = r.decoded_value
    let head = ''
    if (dv.expires_at) {
      head = '<div class="schema-head">过期时间: ' + esc(dv.expires_at) +
        (dv.expired ? ' <span class="badge warn">已过期</span>' : ' <span class="badge ok">存活</span>') + '</div>'
    }
    const rows = (dv.fields || [])
      .map((f) => '<tr><td>' + esc(f.name) + '</td><td>' + esc(f.type) + '</td><td class="val">' +
        esc(f.value) + (f.truncated ? ' <span class="hint">(截断)</span>' : '') + '</td></tr>')
      .join('')
    semantic = head + '<table class="fields"><thead><tr><th>字段</th><th>类型</th><th>值</th></tr></thead><tbody>' + rows + '</tbody></table>'
  }

  document.getElementById('dTitle').textContent = ''
  box.innerHTML = '<h3 id="dTitle">' + esc(title) + '</h3>' +
    (semantic || '') +
    (r.value_truncated ? '<div class="schema-head"><span class="badge warn">原始字节已截断：仅展示前 ' + fmtBytes(Math.floor(r.value_hex.length / 2)) + ' / 共 ' + r.size + ' 字节</span></div>' : '') +
    '<h3 style="margin-top:12px">原始字节</h3><pre id="dBody">' +
    esc(r.value_hex ? hexDump(r.value_hex, r.value_print) : '(空)') + '</pre>'
}

// 将十六进制串渲染为 offset + 16 字节/行的经典转储格式
function hexDump(hexStr, printStr) {
  const bytes = new Uint8Array(hexStr.match(/.{2}/g).map((h) => parseInt(h, 16)))
  let out = ''
  for (let off = 0; off < bytes.length; off += 16) {
    const slice = bytes.subarray(off, off + 16)
    const hex = Array.from(slice)
      .map((b) => b.toString(16).padStart(2, '0'))
      .join(' ')
    out += off.toString(16).padStart(8, '0') + '  ' + hex + '\n'
  }
  if (printStr) out += '\n可读形式: ' + printStr
  return out
}

// ---- 事件绑定与初始化 ----

document.getElementById('protoFile').addEventListener('change', (e) => {
  if (e.target.files.length) importSchema(e.target.files[0])
})

// 切表时同步模式 UI（表模式禁用前缀输入）
document.getElementById('tableSel').addEventListener('change', syncModeUI)
syncModeUI()

// inline onclick 需要
window.loadKeys = loadKeys
window.nextPage = nextPage
window.prevPage = prevPage
window.showDetail = showDetail
window.clearSchema = clearSchema

loadStats()
loadSchema()
