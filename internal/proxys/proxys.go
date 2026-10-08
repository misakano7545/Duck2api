package proxys

import "sync"

type Identity struct {
	Proxy string
	UA    string
}

type IProxy struct {
	idents []Identity
	lock   sync.Mutex
}

func NewIProxyIP(idents []Identity) IProxy {
	return IProxy{
		idents: idents,
	}
}

// GetIdentity 轮转取一个身份。
//
// Identity 里的出口和 UA 必须成对使用: 上游按客户端指纹限速, 而指纹里既有出口 IP
// 也有 UA —— 拿 A 的 token 配 B 的 UA 发出去, 挑战复算必然对不上(418)。所以这里
// 一次取一整个身份, 不要在调用点各自拼 proxy 和 ua。
func (p *IProxy) GetIdentity() Identity {
	if p == nil {
		return Identity{}
	}

	p.lock.Lock()
	defer p.lock.Unlock()

	if len(p.idents) == 0 {
		return Identity{}
	}

	// 取走队首, 挪到队尾 —— 与旧 GetProxyIP 同一套轮询。
	ident := p.idents[0]
	p.idents = append(p.idents[1:], ident)
	return ident
}
