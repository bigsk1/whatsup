import type { PaletteId, Skin } from './catalog';

export const look = $state<{ skin: Skin; palette: PaletteId }>({
  skin: 'bubble',
  palette: 'whatsup',
});
