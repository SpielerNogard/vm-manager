// Mirrors the hub's JSON (internal/hub, internal/proto).

export interface Node {
  id: string
  hostname: string
  provider: string
  cpus: number
  memoryMB: number
  freeCpus: number
  freeMemoryMB: number
  online: boolean
  connectedAt: string
  /** The node's config file without the hub token. */
  config?: Record<string, unknown>
  machine?: Machine
  chassis?: Chassis
}

export interface Chassis {
  ip: string
  lastHandshake?: string
  registered: boolean
}

export interface Machine {
  os: string
  osVersion?: string
  arch: string
  cpuModel?: string
  cpus: number
  memoryMB?: number
}

export type VMState = 'pending' | 'starting' | 'running' | 'stopped' | 'error' | 'unknown'

export interface DataDisk {
  name: string
  mountPath: string
  sizeGB: number
}

export interface VM {
  id: string
  name: string
  cpus: number
  memoryMB: number
  diskGB: number
  dataDisks?: DataDisk[]
  sshPublicKeys: string[]
  nics?: NIC[]
  nodeId: string
  provider: string
  state: VMState
  ip?: string
  error?: string
}

export interface NIC {
  network: string
  mac: string
  ip: string
  port: string
}

export interface Network {
  name: string
  subnet: string
  members: number
}

export interface Device {
  id: string
  name: string
  ip: string
  publicKey: string
  createdAt: string
}

export interface SSHKey {
  id: string
  name: string
  publicKey: string
  type: string
  fingerprint: string
  hub?: boolean
}

export interface CreateVMRequest {
  nodeId: string
  name: string
  cpus: number
  memoryMB: number
  diskGB: number
  dataDisks: DataDisk[]
  sshKeyIds: string[]
}

async function text(path: string): Promise<string> {
  const res = await fetch(path)
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText)
  return res.text()
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || res.statusText)
  return data as T
}

export const api = {
  nodes: () => request<Node[]>('GET', '/api/nodes'),
  vms: () => request<VM[]>('GET', '/api/vms'),
  createVM: (req: CreateVMRequest) => request<VM>('POST', '/api/vms', req),
  deleteVM: (id: string) => request<void>('DELETE', `/api/vms/${id}`),
  keys: () => request<SSHKey[]>('GET', '/api/keys'),
  addKey: (name: string, publicKey: string) => request<SSHKey>('POST', '/api/keys', { name, publicKey }),
  deleteKey: (id: string) => request<void>('DELETE', `/api/keys/${id}`),
  networks: () => request<Network[]>('GET', '/api/networks'),
  devices: () => request<Device[]>('GET', '/api/devices'),
  addDevice: (name: string) => request<{ device: Device; config: string }>('POST', '/api/devices', { name }),
  deleteDevice: (id: string) => request<void>('DELETE', `/api/devices/${id}`),
  deviceConfig: (id: string) => text(`/api/devices/${id}/config`),
}
