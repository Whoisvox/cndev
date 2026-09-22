# gap
1. 分析increase外推

## 改中间件顺序
1. withObservaility(withRecovery(withAuthenticator(TimeoutHandler(mux))))
panic路径是 mux panic -> TimeoutHandler搬运到下一层重抛 -> authenticator -> withRecovery接住，writeHeader(500) -> request行按ERROR打，Inc(status=500)

