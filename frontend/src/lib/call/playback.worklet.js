// @ts-nocheck
// Playback for a voice call. A small ring buffer absorbs uneven arrival: it
// waits for a little audio before starting, plays silence if it runs dry, and
// skips ahead if too much piles up, so delay cannot grow without bound.
const CAPACITY = 48000; // one second at 48 kHz
const START_AT = 2880; // 60 ms buffered before playback begins
const TOO_MUCH = 14400; // 300 ms: drop the oldest audio beyond this
const KEEP = 4800; // 100 ms kept after dropping

class CallPlaybackProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buffer = new Float32Array(CAPACITY);
    this.read = 0;
    this.write = 0;
    this.size = 0;
    this.playing = false;
    // Counters for the call details readout: numbers only, never audio.
    this.played = 0;
    this.quanta = 0;
    this.port.onmessage = (event) => {
      const samples = event.data;
      if (!(samples instanceof Float32Array) || samples.length > CAPACITY)
        return;
      for (let i = 0; i < samples.length; i++) {
        this.buffer[this.write] = samples[i];
        this.write = (this.write + 1) % CAPACITY;
      }
      this.size = Math.min(CAPACITY, this.size + samples.length);
      if (this.size === CAPACITY) this.read = this.write;
      if (this.size > TOO_MUCH) {
        const drop = this.size - KEEP;
        this.read = (this.read + drop) % CAPACITY;
        this.size = KEEP;
      }
    };
  }

  process(_inputs, outputs) {
    // About twice a second, say how many samples have been played so far.
    this.quanta++;
    if (this.quanta % 188 === 0)
      this.port.postMessage({ played: this.played, quanta: this.quanta });
    const output = outputs[0] && outputs[0][0];
    if (!output) return true;
    if (!this.playing && this.size >= START_AT) this.playing = true;
    if (!this.playing) return true; // silence while buffering
    for (let i = 0; i < output.length; i++) {
      if (this.size === 0) {
        // Ran dry: go back to buffering so the next audio is not choppy.
        this.playing = false;
        break;
      }
      output[i] = this.buffer[this.read];
      this.read = (this.read + 1) % CAPACITY;
      this.size--;
      this.played++;
    }
    return true;
  }
}

registerProcessor('whatsup-call-playback', CallPlaybackProcessor);
