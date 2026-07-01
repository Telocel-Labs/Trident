# Webhook support

## Signature verification

Subscribers should verify the `X-Trident-Signature` header with a constant-time comparison:

```ts
import { createHmac, timingSafeEqual } from 'crypto';

function verifyWebhookSignature(body: string, signature: string, secret: string): boolean {
  const expected = 'sha256=' + createHmac('sha256', secret).update(body).digest('hex');
  return timingSafeEqual(Buffer.from(signature), Buffer.from(expected));
}
```
