// 16x16 pixel mascot. Legend: t tile, b bubble, e eye, m mouth, s spark.
const rows = [
  '.tttttttttttstt.',
  'tttttttttttssstt',
  'tttbbbbbbbbtsttt',
  'ttbbbbbbbbbbbttt',
  'tbbbbbbbbbbbbbtt',
  'tbbbeebbbeebbbtt',
  'tbbbeebbbeebbbtt',
  'tbbbbbbbbbbbbbtt',
  'tbbbmbbbbbmbbbtt',
  'tbbbbmmmmmbbbbtt',
  'tbbbbbbbbbbbbbtt',
  'ttbbbbbbbbbbbttt',
  'tttbbbbbbbbttttt',
  'tttbbttttttttttt',
  'ttbttttttttttttt',
  '.tttttttttttttt.',
];

export type PixelTone = 'tile' | 'bubble' | 'eye' | 'mouth' | 'spark';

const tones: Record<string, PixelTone> = {
  t: 'tile',
  b: 'bubble',
  e: 'eye',
  m: 'mouth',
  s: 'spark',
};

export interface PixelRun {
  x: number;
  y: number;
  width: number;
  tone: PixelTone;
}

export const pixelGrid = 16;

export const pixelRuns: PixelRun[] = rows.flatMap((row, y) => {
  const runs: PixelRun[] = [];
  let x = 0;
  while (x < row.length) {
    const tone = tones[row[x]];
    let end = x + 1;
    while (end < row.length && row[end] === row[x]) end += 1;
    if (tone) runs.push({ x, y, width: end - x, tone });
    x = end;
  }
  return runs;
});
