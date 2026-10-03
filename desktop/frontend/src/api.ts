export * from '../wailsjs/go/main/App'
export {backend, serversetup} from '../wailsjs/go/models'
export {EventsOn, EventsOff, BrowserOpenURL} from '../wailsjs/runtime/runtime'

export const OBF_PROFILES = ['none', 'rtpopus', 'rtpopus2', 'rtpopus3'] as const
export const TRANSPORTS = ['tcp', 'udp'] as const
export const MODES = ['udp', 'tcp'] as const
export const DNS_MODES = ['auto', 'plain', 'doh'] as const
