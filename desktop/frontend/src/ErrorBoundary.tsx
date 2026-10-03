import {Component, ErrorInfo, ReactNode} from 'react'

interface Props {
  children: ReactNode
}

interface State {
  error: Error | null
}

export default class ErrorBoundary extends Component<Props, State> {
  state: State = {error: null}

  static getDerivedStateFromError(error: Error): State {
    return {error}
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('UI crashed:', error, info.componentStack)
  }

  render() {
    if (this.state.error) {
      return (
        <div style={{padding: 24, color: '#e6e9ef', fontFamily: 'Segoe UI, sans-serif'}}>
          <h2 style={{color: '#f0554f'}}>Интерфейс упал с ошибкой</h2>
          <p style={{color: '#8b93a1'}}>{this.state.error.message}</p>
          <pre style={{whiteSpace: 'pre-wrap', fontSize: 11, color: '#8b93a1', maxHeight: 300, overflow: 'auto'}}>
            {this.state.error.stack}
          </pre>
          <button
            style={{marginTop: 12, padding: '7px 14px', borderRadius: 7, border: '1px solid #2a2f38', background: '#1b1f26', color: '#e6e9ef', cursor: 'pointer'}}
            onClick={() => this.setState({error: null})}
          >
            Попробовать снова
          </button>
        </div>
      )
    }
    return this.props.children
  }
}
