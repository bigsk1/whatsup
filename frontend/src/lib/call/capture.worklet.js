// @ts-nocheck
// Microphone capture for a voice call. Runs on the audio thread, which keeps
// running when the window is hidden. It gathers mono input into 20 ms frames
// (960 samples at 48 kHz) and hands each frame to the page. Nothing is stored.
class CallCaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.frame = new Float32Array(960);
    this.filled = 0;
    this.muted = false;
    this.port.onmessage = (event) => {
      if (event.data && typeof event.data.muted === 'boolean')
        this.muted = event.data.muted;
    };
  }

  process(inputs) {
    const channel = inputs[0] && inputs[0][0];
    if (!channel) return true;
    let offset = 0;
    while (offset < channel.length) {
      const take = Math.min(channel.length - offset, 960 - this.filled);
      this.frame.set(channel.subarray(offset, offset + take), this.filled);
      this.filled += take;
      offset += take;
      if (this.filled === 960) {
        this.filled = 0;
        if (this.muted) continue;
        const copy = new Float32Array(960);
        for (let i = 0; i < 960; i++) {
          // Inputs without gain control can exceed full scale. Clip so the
          // encoder never sees out-of-range samples.
          const sample = this.frame[i];
          copy[i] = sample > 1 ? 1 : sample < -1 ? -1 : sample;
        }
        this.port.postMessage(copy, [copy.buffer]);
      }
    }
    return true;
  }
}

registerProcessor('whatsup-call-capture', CallCaptureProcessor);
