// One list for the router and the sidebar, so a nav entry never points at a missing route.

export type Section = 'nodes' | 'vms' | 'keys' | 'devices'

export interface NavItem {
  name: Section
  path: string
  icon: string
}

export interface NavGroup {
  group: 'fleet' | 'access'
  items: NavItem[]
}

export const NAV: NavGroup[] = [
  {
    group: 'fleet',
    items: [
      { name: 'nodes', path: '/nodes', icon: 'server' },
      { name: 'vms', path: '/vms', icon: 'cube' },
    ],
  },
  {
    group: 'access',
    items: [
      { name: 'keys', path: '/keys', icon: 'key' },
      { name: 'devices', path: '/devices', icon: 'shield' },
    ],
  },
]

export const NAV_ITEMS = NAV.flatMap((g) => g.items)
