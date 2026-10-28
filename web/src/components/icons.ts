/** Central registry of the app's chrome icons (Lucide). UI chrome — controls, navigation, empty states — draws
 *  from here so stroke style stays consistent; emoji or symbols coming from user data (bdb names, formula display
 *  content) are content, never replaced by icons. Add icons as named re-exports, keeping the list alphabetical. */

export type { LucideIcon } from "lucide-react";

export {
  ArrowDownUp,
  BookOpen,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Columns3,
  Copy,
  Database,
  Ellipsis,
  GripVertical,
  Inbox,
  Info,
  Menu as MenuIcon,
  Moon,
  Plus,
  Rows3,
  SquareKanban,
  Sun,
  Table2,
  Users,
  X,
} from "lucide-react";
