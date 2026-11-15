import AddressBook from 'phosphor-svelte/lib/AddressBook';
import ArrowClockwise from 'phosphor-svelte/lib/ArrowClockwise';
import ArrowLeft from 'phosphor-svelte/lib/ArrowLeft';
import Bell from 'phosphor-svelte/lib/Bell';
import CaretDown from 'phosphor-svelte/lib/CaretDown';
import CaretRight from 'phosphor-svelte/lib/CaretRight';
import ChatCircleText from 'phosphor-svelte/lib/ChatCircleText';
import ChatsCircle from 'phosphor-svelte/lib/ChatsCircle';
import Check from 'phosphor-svelte/lib/Check';
import Checks from 'phosphor-svelte/lib/Checks';
import Copy from 'phosphor-svelte/lib/Copy';
import DotsThree from 'phosphor-svelte/lib/DotsThree';
import DownloadSimple from 'phosphor-svelte/lib/DownloadSimple';
import Eraser from 'phosphor-svelte/lib/Eraser';
import Eye from 'phosphor-svelte/lib/Eye';
import File from 'phosphor-svelte/lib/File';
import FileArrowDown from 'phosphor-svelte/lib/FileArrowDown';
import FolderOpen from 'phosphor-svelte/lib/FolderOpen';
import GameController from 'phosphor-svelte/lib/GameController';
import GearSix from 'phosphor-svelte/lib/GearSix';
import Globe from 'phosphor-svelte/lib/Globe';
import Heart from 'phosphor-svelte/lib/Heart';
import Image from 'phosphor-svelte/lib/Image';
import Info from 'phosphor-svelte/lib/Info';
import Key from 'phosphor-svelte/lib/Key';
import LinkSimple from 'phosphor-svelte/lib/LinkSimple';
import LockSimple from 'phosphor-svelte/lib/LockSimple';
import LockSimpleOpen from 'phosphor-svelte/lib/LockSimpleOpen';
import MagnifyingGlass from 'phosphor-svelte/lib/MagnifyingGlass';
import MagnifyingGlassMinus from 'phosphor-svelte/lib/MagnifyingGlassMinus';
import MagnifyingGlassPlus from 'phosphor-svelte/lib/MagnifyingGlassPlus';
import Palette from 'phosphor-svelte/lib/Palette';
import PaperPlaneRight from 'phosphor-svelte/lib/PaperPlaneRight';
import Paperclip from 'phosphor-svelte/lib/Paperclip';
import Pause from 'phosphor-svelte/lib/Pause';
import Play from 'phosphor-svelte/lib/Play';
import Plug from 'phosphor-svelte/lib/Plug';
import Plus from 'phosphor-svelte/lib/Plus';
import Power from 'phosphor-svelte/lib/Power';
import Question from 'phosphor-svelte/lib/Question';
import ShieldCheck from 'phosphor-svelte/lib/ShieldCheck';
import Smiley from 'phosphor-svelte/lib/Smiley';
import Sparkle from 'phosphor-svelte/lib/Sparkle';
import Trash from 'phosphor-svelte/lib/Trash';
import UserCircle from 'phosphor-svelte/lib/UserCircle';
import UserMinus from 'phosphor-svelte/lib/UserMinus';
import UsersThree from 'phosphor-svelte/lib/UsersThree';
import Warning from 'phosphor-svelte/lib/Warning';
import WifiHigh from 'phosphor-svelte/lib/WifiHigh';
import WifiSlash from 'phosphor-svelte/lib/WifiSlash';
import X from 'phosphor-svelte/lib/X';
import type { pixelPaths } from './pixel-paths';

export type IconName = keyof typeof pixelPaths;

export const phosphorIcons = {
  chat: ChatCircleText,
  chats: ChatsCircle,
  users: UsersThree,
  user: UserCircle,
  plus: Plus,
  search: MagnifyingGlass,
  close: X,
  settings: GearSix,
  info: Info,
  help: Question,
  lock: LockSimple,
  unlock: LockSimpleOpen,
  power: Power,
  send: PaperPlaneRight,
  attach: Paperclip,
  smile: Smiley,
  image: Image,
  file: File,
  download: DownloadSimple,
  check: Check,
  checks: Checks,
  trash: Trash,
  eraser: Eraser,
  forget: UserMinus,
  wifi: WifiHigh,
  wifiOff: WifiSlash,
  chevronDown: CaretDown,
  chevronRight: CaretRight,
  arrowLeft: ArrowLeft,
  palette: Palette,
  shield: ShieldCheck,
  bell: Bell,
  play: Play,
  pause: Pause,
  more: DotsThree,
  copy: Copy,
  link: LinkSimple,
  plug: Plug,
  sparkle: Sparkle,
  folder: FolderOpen,
  warning: Warning,
  key: Key,
  globe: Globe,
  zoomIn: MagnifyingGlassPlus,
  zoomOut: MagnifyingGlassMinus,
  contact: AddressBook,
  heart: Heart,
  reload: ArrowClockwise,
  dragDrop: FileArrowDown,
  gamepad: GameController,
  eye: Eye,
} satisfies Record<IconName, unknown>;
