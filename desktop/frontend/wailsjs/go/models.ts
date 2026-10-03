export namespace backend {
	
	export class GuestShare {
	    pub: string;
	    name: string;
	    ip: string;
	    link: string;
	    qr: string;
	    includesVk: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GuestShare(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pub = source["pub"];
	        this.name = source["name"];
	        this.ip = source["ip"];
	        this.link = source["link"];
	        this.qr = source["qr"];
	        this.includesVk = source["includesVk"];
	    }
	}
	export class KCPProfile {
	    noDelay: number;
	    interval: number;
	    resend: number;
	    nc: number;
	    sndWnd: number;
	    rcvWnd: number;
	    mtu: number;
	    ackNoDelay: boolean;
	
	    static createFrom(source: any = {}) {
	        return new KCPProfile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.noDelay = source["noDelay"];
	        this.interval = source["interval"];
	        this.resend = source["resend"];
	        this.nc = source["nc"];
	        this.sndWnd = source["sndWnd"];
	        this.rcvWnd = source["rcvWnd"];
	        this.mtu = source["mtu"];
	        this.ackNoDelay = source["ackNoDelay"];
	    }
	}
	export class Server {
	    id: string;
	    name: string;
	    peer: string;
	    provider: string;
	    vkLinks: string[];
	    transport: string;
	    mode: string;
	    obfProfile: string;
	    obfKey: string;
	    obfTimingMs: number;
	    n: number;
	    streamsPerCred: number;
	    clientId: string;
	    listen: string;
	    dnsMode: string;
	    dnsServers: string[];
	    manualCaptcha: boolean;
	    debug: boolean;
	    routes: boolean;
	    turnHost: string;
	    turnPort: string;
	    kcp: KCPProfile;
	    ssh?: serversetup.SSHConfig;
	    wgClientConf?: string;
	    wgPort?: number;
	    connMode?: string;
	    // Go type: time
	    createdAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Server(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.peer = source["peer"];
	        this.provider = source["provider"];
	        this.vkLinks = source["vkLinks"];
	        this.transport = source["transport"];
	        this.mode = source["mode"];
	        this.obfProfile = source["obfProfile"];
	        this.obfKey = source["obfKey"];
	        this.obfTimingMs = source["obfTimingMs"];
	        this.n = source["n"];
	        this.streamsPerCred = source["streamsPerCred"];
	        this.clientId = source["clientId"];
	        this.listen = source["listen"];
	        this.dnsMode = source["dnsMode"];
	        this.dnsServers = source["dnsServers"];
	        this.manualCaptcha = source["manualCaptcha"];
	        this.debug = source["debug"];
	        this.routes = source["routes"];
	        this.turnHost = source["turnHost"];
	        this.turnPort = source["turnPort"];
	        this.kcp = this.convertValues(source["kcp"], KCPProfile);
	        this.ssh = this.convertValues(source["ssh"], serversetup.SSHConfig);
	        this.wgClientConf = source["wgClientConf"];
	        this.wgPort = source["wgPort"];
	        this.connMode = source["connMode"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ServerSetupDraft {
	    name: string;
	    vkLink: string;
	    obfProfile: string;
	    obfKey: string;
	    obfTimingMs: number;
	    listenPort: number;
	    clientId: string;
	
	    static createFrom(source: any = {}) {
	        return new ServerSetupDraft(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.vkLink = source["vkLink"];
	        this.obfProfile = source["obfProfile"];
	        this.obfKey = source["obfKey"];
	        this.obfTimingMs = source["obfTimingMs"];
	        this.listenPort = source["listenPort"];
	        this.clientId = source["clientId"];
	    }
	}
	export class ShareLink {
	    provider: string;
	    peer: string;
	    transport: string;
	    mode: string;
	    obfProfile: string;
	    obfKey: string;
	    n: number;
	    streamsPerCred: number;
	    clientId: string;
	    listen: string;
	    dnsMode: string;
	    dnsServers: string[];
	    manualCaptcha: boolean;
	    kcp?: KCPProfile;
	    name: string;
	    vkLink: string;
	    wgConf: string;
	
	    static createFrom(source: any = {}) {
	        return new ShareLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.peer = source["peer"];
	        this.transport = source["transport"];
	        this.mode = source["mode"];
	        this.obfProfile = source["obfProfile"];
	        this.obfKey = source["obfKey"];
	        this.n = source["n"];
	        this.streamsPerCred = source["streamsPerCred"];
	        this.clientId = source["clientId"];
	        this.listen = source["listen"];
	        this.dnsMode = source["dnsMode"];
	        this.dnsServers = source["dnsServers"];
	        this.manualCaptcha = source["manualCaptcha"];
	        this.kcp = this.convertValues(source["kcp"], KCPProfile);
	        this.name = source["name"];
	        this.vkLink = source["vkLink"];
	        this.wgConf = source["wgConf"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Status {
	    state: string;
	    streams: number;
	    total: number;
	    errMsg: string;
	    txRate: number;
	    rxRate: number;
	    txTotal: number;
	    rxTotal: number;
	    serverId: string;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.streams = source["streams"];
	        this.total = source["total"];
	        this.errMsg = source["errMsg"];
	        this.txRate = source["txRate"];
	        this.rxRate = source["rxRate"];
	        this.txTotal = source["txTotal"];
	        this.rxTotal = source["rxTotal"];
	        this.serverId = source["serverId"];
	    }
	}

}

export namespace serversetup {
	
	export class InstallResult {
	    version: string;
	    runtime: string;
	    needsRestart: boolean;
	
	    static createFrom(source: any = {}) {
	        return new InstallResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.runtime = source["runtime"];
	        this.needsRestart = source["needsRestart"];
	    }
	}
	export class Peer {
	    pub: string;
	    name: string;
	    ip: string;
	    lastHandshake: number;
	    rx: number;
	    tx: number;
	    hasConf: boolean;
	    isOwner: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Peer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pub = source["pub"];
	        this.name = source["name"];
	        this.ip = source["ip"];
	        this.lastHandshake = source["lastHandshake"];
	        this.rx = source["rx"];
	        this.tx = source["tx"];
	        this.hasConf = source["hasConf"];
	        this.isOwner = source["isOwner"];
	    }
	}
	export class PeerList {
	    peers: Peer[];
	    serverNow: number;
	
	    static createFrom(source: any = {}) {
	        return new PeerList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.peers = this.convertValues(source["peers"], Peer);
	        this.serverNow = source["serverNow"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ProbeResult {
	    hostFingerprint: string;
	    installed: boolean;
	    running: boolean;
	    wgPort: number;
	
	    static createFrom(source: any = {}) {
	        return new ProbeResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostFingerprint = source["hostFingerprint"];
	        this.installed = source["installed"];
	        this.running = source["running"];
	        this.wgPort = source["wgPort"];
	    }
	}
	export class SSHConfig {
	    ip: string;
	    port: number;
	    username: string;
	    password: string;
	    authType: string;
	    sshKey: string;
	    hostFingerprint: string;
	    rootMode: string;
	    sudoPassword: string;
	
	    static createFrom(source: any = {}) {
	        return new SSHConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ip = source["ip"];
	        this.port = source["port"];
	        this.username = source["username"];
	        this.password = source["password"];
	        this.authType = source["authType"];
	        this.sshKey = source["sshKey"];
	        this.hostFingerprint = source["hostFingerprint"];
	        this.rootMode = source["rootMode"];
	        this.sudoPassword = source["sudoPassword"];
	    }
	}
	export class StartOptions {
	    listen: string;
	    connect: string;
	    obfProfile: string;
	    obfKey: string;
	    obfTimingMs: number;
	    clientId: string;
	
	    static createFrom(source: any = {}) {
	        return new StartOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.listen = source["listen"];
	        this.connect = source["connect"];
	        this.obfProfile = source["obfProfile"];
	        this.obfKey = source["obfKey"];
	        this.obfTimingMs = source["obfTimingMs"];
	        this.clientId = source["clientId"];
	    }
	}
	export class UninstallRemoved {
	    binary: boolean;
	    unit: boolean;
	    wg_iface: boolean;
	    prefix: boolean;
	    sysctl: boolean;
	    ufw: boolean;
	    legacy_unit: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UninstallRemoved(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.binary = source["binary"];
	        this.unit = source["unit"];
	        this.wg_iface = source["wg_iface"];
	        this.prefix = source["prefix"];
	        this.sysctl = source["sysctl"];
	        this.ufw = source["ufw"];
	        this.legacy_unit = source["legacy_unit"];
	    }
	}
	export class UninstallResult {
	    dryRun: boolean;
	    removed: UninstallRemoved;
	    wgPkgRemoved: boolean;
	    kept: string[];
	
	    static createFrom(source: any = {}) {
	        return new UninstallResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.dryRun = source["dryRun"];
	        this.removed = this.convertValues(source["removed"], UninstallRemoved);
	        this.wgPkgRemoved = source["wgPkgRemoved"];
	        this.kept = source["kept"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WgSetupResult {
	    port: number;
	    clientConf: string;
	    existed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WgSetupResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.port = source["port"];
	        this.clientConf = source["clientConf"];
	        this.existed = source["existed"];
	    }
	}

}

