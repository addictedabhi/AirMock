// Generates a ready-to-run example command for "how do I actually talk to
// this mock" — shown in each mock type's expanded detail view. Every
// function returns a single multi-line string meant for one copyable code
// block, using the mock's own real port/path/rule so the example is
// immediately runnable, not a generic placeholder.

// clientCertHint appends the flags/notes needed once a listener's
// ClientCertMode is "optional" or "required" — without this, "how to test
// this" would show a command that fails outright against a required-mTLS
// listener (or silently skips exercising verification against an optional
// one), with no indication of what's actually needed to get past the
// handshake. certFlags/keyFlag are the tool-specific flag names (curl:
// --cert/--key, openssl s_client: -cert/-key) since the placeholder
// filenames are the same either way — download a client cert issued by the
// configured CA from the Certificates page first.
function clientCertHint(tls, certFlag, keyFlag) {
  // A saved client-cert mode only means something while TLS itself is on: the
  // gateway keeps its mode when HTTPS is switched off, and the hint then
  // contradicted the plain http:// command above it. (TCP/SMTP tls objects
  // have no `enabled` field; their presence means on.)
  if (!tls || tls.enabled === false) return '';
  if (tls.clientCertMode !== 'optional' && tls.clientCertMode !== 'required') return '';
  const requirement = tls.clientCertMode === 'required' ? 'required' : 'optional, but verified if provided';
  return (
    `\n# Client certificate ${requirement} — download one issued by the configured CA from the\n` +
    `# Certificates page (or generate a bundle with a client cert), then add:\n` +
    `# ${certFlag} client.pem ${keyFlag} client-key.pem`
  );
}

export function usageForRest(mock, gatewayPort, tls) {
  const scheme = tls?.enabled ? 'https' : 'http';
  const url = `${scheme}://localhost:${gatewayPort}${mock.pathPattern}`;
  const method = mock.method || 'GET';
  const cmd =
    method === 'GET' || method === 'HEAD'
      ? `curl -X ${method} '${url}'`
      : `curl -X ${method} '${url}' \\\n  -H 'Content-Type: application/json' \\\n  -d '{}'`;
  return cmd + clientCertHint(tls, '--cert', '--key');
}

export function usageForSoap(mock, gatewayPort, tls) {
  const scheme = tls?.enabled ? 'https' : 'http';
  const url = `${scheme}://localhost:${gatewayPort}${mock.pathPattern}`;
  const action = mock.soapAction || mock.operationName || 'YourOperation';
  return (
    `curl -X POST '${url}' \\\n` +
    `  -H 'Content-Type: text/xml' \\\n` +
    `  -H 'SOAPAction: "${action}"' \\\n` +
    `  -d '<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">\n` +
    `  <soap:Body><${mock.operationName || 'YourOperation'}/></soap:Body>\n</soap:Envelope>'` +
    clientCertHint(tls, '--cert', '--key')
  );
}

export function usageForGraphql(mock, gatewayPort, tls) {
  const scheme = tls?.enabled ? 'https' : 'http';
  const url = `${scheme}://localhost:${gatewayPort}${mock.pathPattern}`;
  const op = mock.operationName || 'yourQuery';
  return (
    `curl -X POST '${url}' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"query":"{ ${op} }"}'` +
    clientCertHint(tls, '--cert', '--key')
  );
}

export function usageForWs(mock, gatewayPort, tls) {
  const scheme = tls?.enabled ? 'wss' : 'ws';
  const url = `${scheme}://localhost:${gatewayPort}${mock.pathPattern}`;
  return `wscat -c '${url}'\n# (or: websocat '${url}')` + clientCertHint(tls, '--cert-file', '--key-file');
}

export function usageForTcp(mock) {
  const port = mock.tcp?.port;
  const tls = mock.tcp?.tls;
  if (tls) {
    return (
      `openssl s_client -connect localhost:${port}\n` +
      `# (telnet/nc won't work here — this port speaks TLS, not plaintext)` +
      clientCertHint(tls, '-cert', '-key')
    );
  }
  return `telnet localhost ${port}\n# (or: nc localhost ${port})`;
}

export function usageForSmtp(mock) {
  const port = mock.smtp?.port;
  const tls = mock.smtp?.tls;
  if (tls) {
    return (
      `swaks --to test@example.com --from you@example.com --server localhost:${port} -tlsc\n` +
      `# -tlsc: this mock's listener speaks implicit TLS (like a real port-465 relay),\n` +
      `# not STARTTLS` +
      clientCertHint(tls, '--tls-cert', '--tls-key')
    );
  }
  return (
    `swaks --to test@example.com --from you@example.com --server localhost:${port}\n` +
    `# (or point any SMTP client's host/port at localhost:${port} — no TLS unless this mock has it enabled)`
  );
}

export function usageForMqtt(mock) {
  const port = mock.mqtt?.port;
  const rule = mock.mqtt?.rules?.[0];
  const topic = (rule?.topicPattern || 'test/topic').replace(/[+#]/g, 'test');
  return (
    `mosquitto_sub -h localhost -p ${port} -t '#' &\n` +
    `mosquitto_pub -h localhost -p ${port} -t '${topic}' -m 'hello'`
  );
}

export function usageForKafka(mock) {
  const port = mock.kafka?.port;
  const rule = mock.kafka?.rules?.[0];
  const topic = rule?.topicPattern || 'test-topic';
  return (
    `# requires kcat (https://github.com/edenhill/kcat)\n` +
    `kcat -b localhost:${port} -t '${topic}' -P -e <<< 'hello'   # produce\n` +
    `kcat -b localhost:${port} -t '${topic}' -C -o beginning     # consume`
  );
}

export function usageForSmpp(mock) {
  const port = mock.smpp?.port;
  const rule = mock.smpp?.rules?.[0];
  const dest = rule?.destAddrPattern || '2000';
  const credsNote = mock.smpp?.systemId ? `system_id: ${mock.smpp.systemId}` : 'any system_id/password accepted';
  return (
    `# requires an SMPP test client, e.g. smppload or a small go-smpp/smpplib script\n` +
    `# bind_transceiver to localhost:${port} (${credsNote})\n` +
    `# then submit_sm with destination_addr='${dest}' to trigger this mock's rules`
  );
}

export function usageForDiameter(mock) {
  const port = mock.diameter?.port;
  const realm = mock.diameter?.originRealm || 'airmock.test';
  return (
    `# requires a Diameter test client, e.g. github.com/fiorix/go-diameter's examples/client\n` +
    `go run examples/client/client.go -addr localhost:${port} -diam_realm ${realm}\n` +
    `# CER/CEA and DWR/DWA are handled automatically; send a CCR to trigger this mock's rules`
  );
}

export function usageForJms(mock) {
  const port = mock.jms?.port;
  const rule = mock.jms?.rules?.[0];
  const address = rule?.addressPattern || 'orders';
  return (
    `# requires an AMQP 1.0 client — e.g. Qpid JMS, ActiveMQ Artemis's AMQP\n` +
    `# connector, or Python's python-qpid-proton\n` +
    `# connect to amqp://localhost:${port}, send to address '${address}' to trigger this mock's rules\n` +
    `# (no SASL/credentials required)`
  );
}
