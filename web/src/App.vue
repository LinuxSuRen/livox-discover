<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import * as api from './api.js'

const state = ref(null)
const durationSeconds = ref(15)
const probe = ref(true)
const starting = ref(false)
const feedback = ref(null) // {type:'success'|'error'|'info', text}
const form = ref({ lidar: '', ip: '', mask: '255.255.255.0', gw: '' })
const submitting = ref('')
const selected = ref(null)

const devices = computed(() => {
  const s = state.value
  if (!s || !s.report || !Array.isArray(s.report.sources)) return []
  const rows = []
  for (const src of s.report.sources) {
    const ds = Array.isArray(src.discoveries) && src.discoveries.length ? src.discoveries : [null]
    for (const d of ds) {
      rows.push({
        srcIp: src.ip,
        packets: src.packets,
        sn: d ? d.sn : '—',
        dev: d ? d.dev_type_name : '未识别',
        lidarIp: d ? d.lidar_ip : src.ip,
        cmdPort: d ? d.cmd_port : '—',
      })
    }
  }
  return rows
})
const hints = computed(() => (state.value && state.value.hints) || [])
const running = computed(() => !!(state.value && state.value.running))
const remaining = computed(() => {
  const s = state.value
  if (!s || !s.running) return null
  return Math.max(0, Math.ceil(s.duration_seconds - s.elapsed_seconds))
})

let unsubscribe = null
onMounted(() => {
  unsubscribe = api.subscribeScan((s) => {
    state.value = s
  })
})
onUnmounted(() => {
  if (unsubscribe) unsubscribe()
})

function select(row) {
  selected.value = row
  form.value.lidar = row.lidarIp
  form.value.mask = '255.255.255.0'
  form.value.ip = ''
  form.value.gw = ''
  feedback.value = null
}

async function startScan() {
  starting.value = true
  feedback.value = null
  try {
    await api.startScan({ duration_seconds: durationSeconds.value, probe: probe.value })
  } catch (e) {
    feedback.value = { type: 'error', text: e.message }
  } finally {
    starting.value = false
  }
}

async function stopScan() {
  try {
    await api.stopScan()
  } catch (e) {
    feedback.value = { type: 'error', text: e.message }
  }
}

async function submitSetIP() {
  if (!form.value.lidar || !form.value.ip) {
    feedback.value = { type: 'error', text: '请填写雷达当前 IP 和新 IP' }
    return
  }
  submitting.value = 'set-ip'
  try {
    const res = await api.setIP(form.value)
    if (res.ok) {
      feedback.value = {
        type: 'success',
        text: `新 IP ${res.ip} 已保存，重启雷达后生效`,
        rebootIp: form.value.lidar,
      }
    } else {
      feedback.value = { type: 'error', text: '雷达拒绝了该配置，请检查 IP、子网掩码、网关是否一致' }
    }
  } catch (e) {
    feedback.value = { type: 'error', text: e.message }
  } finally {
    submitting.value = ''
  }
}

async function rebootLidar(lidarIp) {
  const lidar = lidarIp || form.value.lidar
  if (!lidar) {
    feedback.value = { type: 'error', text: '请先填写雷达当前 IP' }
    return
  }
  submitting.value = 'reboot'
  try {
    const res = await api.reboot({ lidar })
    if (res.ok) {
      feedback.value = { type: 'success', text: '雷达正在重启，通常一分钟内完成，稍后可重新扫描验证' }
    } else {
      feedback.value = { type: 'error', text: '雷达拒绝了重启请求' }
    }
  } catch (e) {
    feedback.value = { type: 'error', text: e.message }
  } finally {
    submitting.value = ''
  }
}
</script>

<template>
  <div class="page">
    <header class="page__header">
      <h1 class="page__title">livox-discover</h1>
      <p class="page__subtitle">发现 Livox SDK2 雷达，修改雷达 IP，重启雷达</p>
    </header>

    <section class="block">
      <div class="block__head">
        <h2 class="block__title">扫描</h2>
        <span v-if="running" class="tag tag--running">扫描中 · 剩余 {{ remaining }}s</span>
        <span v-else-if="state && state.report && devices.length" class="tag">已发现 {{ devices.length }} 台</span>
      </div>
      <div class="scan-controls">
        <label class="field">
          <span class="field__label">时长</span>
          <select v-model="durationSeconds" :disabled="running" class="field__input">
            <option :value="10">10 秒</option>
            <option :value="15">15 秒</option>
            <option :value="30">30 秒</option>
            <option :value="60">60 秒</option>
          </select>
        </label>
        <label class="field field--inline">
          <input v-model="probe" :disabled="running" type="checkbox" class="field__checkbox" />
          <span class="field__label">主动探测</span>
        </label>
        <button v-if="!running" class="btn btn--primary" :disabled="starting" @click="startScan">
          {{ starting ? '正在启动…' : '开始扫描' }}
        </button>
        <button v-else class="btn btn--danger" @click="stopScan">停止扫描</button>
      </div>
      <p v-if="probe" class="note">主动探测会从 56000 端口向各网段广播发现请求，能找到不主动广播的雷达</p>
    </section>

    <section class="block">
      <div class="block__head">
        <h2 class="block__title">发现的雷达</h2>
      </div>
      <table v-if="devices.length" class="table">
        <thead>
          <tr>
            <th>型号</th>
            <th>序列号</th>
            <th>当前 IP</th>
            <th>命令端口</th>
            <th>包数</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="d in devices"
            :key="d.srcIp + d.sn"
            class="table__row"
            :class="{ 'table__row--selected': selected && selected.sn === d.sn && selected.srcIp === d.srcIp }"
            @click="select(d)"
          >
            <td>{{ d.dev }}</td>
            <td class="mono">{{ d.sn }}</td>
            <td class="mono">{{ d.lidarIp }}</td>
            <td class="mono">{{ d.cmdPort }}</td>
            <td class="mono">{{ d.packets }}</td>
            <td class="table__action">
              <button v-if="d.sn !== '—'" class="btn btn--small" @click.stop="select(d)">操作</button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else-if="state && !running" class="empty">尚未发现雷达。确认雷达已上电并与本机同网段后，尝试勾选主动探测重新扫描。</p>
      <p v-else-if="!state" class="empty">正在连接后端服务…</p>
      <ul v-if="hints.length" class="hints">
        <li v-for="h in hints" :key="h" class="hints__item">{{ h }}</li>
      </ul>
    </section>

    <section class="block">
      <div class="block__head">
        <h2 class="block__title">雷达操作</h2>
        <span v-if="selected" class="tag">已选中 {{ selected.dev }} · {{ selected.lidarIp }}</span>
      </div>
      <div class="form">
        <label class="field">
          <span class="field__label">雷达当前 IP</span>
          <input v-model="form.lidar" class="field__input mono" placeholder="例如 192.168.144.156" />
        </label>
        <label class="field">
          <span class="field__label">新 IP</span>
          <input v-model="form.ip" class="field__input mono" placeholder="例如 192.168.144.160" />
        </label>
        <label class="field">
          <span class="field__label">子网掩码</span>
          <input v-model="form.mask" class="field__input mono" />
        </label>
        <label class="field">
          <span class="field__label">网关</span>
          <input v-model="form.gw" class="field__input mono" placeholder="留空自动推导（新 IP 前三段 + 1）" />
        </label>
      </div>
      <div class="actions">
        <button class="btn btn--primary" :disabled="submitting !== ''" @click="submitSetIP">
          {{ submitting === 'set-ip' ? '正在修改…' : '修改 IP' }}
        </button>
        <button class="btn btn--outline" :disabled="submitting !== ''" @click="rebootLidar()">
          {{ submitting === 'reboot' ? '正在重启…' : '重启雷达' }}
        </button>
      </div>
      <p class="note">修改 IP 保存后需重启雷达才会生效；重启用雷达当前 IP，不是新 IP</p>

      <div v-if="feedback" class="feedback" :class="`feedback--${feedback.type}`">
        <span>{{ feedback.text }}</span>
        <button
          v-if="feedback.rebootIp"
          class="btn btn--small btn--outline"
          :disabled="submitting !== ''"
          @click="rebootLidar(feedback.rebootIp)"
        >
          立即重启
        </button>
      </div>
    </section>
  </div>
</template>
