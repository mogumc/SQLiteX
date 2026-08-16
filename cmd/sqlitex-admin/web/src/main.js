import './style.css'

let cursors = []
let nextCursor = ''
let prefix = ''

const fmtBytes = (n) =>
  n == null ? '-' : n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB'

const esc = (s) =>
  String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

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

function loadKeys() {
  prefix = document.getElementById('prefix').value
  cursors = ['']
  fetchPage('')
}

async function fetchPage(cur) {
  const u = new URL('/api/keys', location)
  u.searchParams.set('prefix', prefix)
  if (cur) u.searchParams.set('cursor', cur)
  const r = await (await fetch(u)).json()
  const tb = document.getElementById('rows')
  if (!r.entries || !r.entries.length) {
    tb.innerHTML = '<tr><td colspan="3" class="hint">无数据</td></tr>'
  } else {
    tb.innerHTML = r.entries
      .map(
        (e) =>
          '<tr class="row" onclick="showDetail(\'' +
          e.key_b64 +
          '\')"><td>' +
          esc(e.key) +
          '</td><td class="num">' +
          e.size +
          '</td><td class="hint">' +
          esc(e.preview) +
          '</td></tr>',
      )
      .join('')
  }
  nextCursor = r.next_cursor || ''
  document.getElementById('next').style.visibility = nextCursor ? 'visible' : 'hidden'
  document.getElementById('prev').style.visibility = cursors.length > 1 ? 'visible' : 'hidden'
  document.getElementById('pageinfo').textContent = (r.entries ? r.entries.length : 0) + ' 条 / 页'
}

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

async function showDetail(b64) {
  const r = await (await fetch('/api/key?k=' + b64)).json()
  const box = document.getElementById('detail')
  box.style.display = 'block'
  document.getElementById('dTitle').textContent =
    '记录详情 · ' + (r.key_print || r.key_hex) + ' · ' + r.size + ' 字节'
  document.getElementById('dBody').textContent = r.value_hex ? hexDump(r.value_hex, r.value_print) : '(空)'
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

// inline onclick 需要
window.loadKeys = loadKeys
window.nextPage = nextPage
window.prevPage = prevPage
window.showDetail = showDetail

loadStats()
