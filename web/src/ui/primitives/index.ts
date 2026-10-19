/*
 * Material 3 primitives, shared by every screen: the barrel only.
 *
 * Each family lives in its own file and imports its neighbours from those
 * files directly, never from here — this module used to be the library itself,
 * and two of the files beside it imported it back, which is a cycle the
 * bundler happened to resolve. Nothing here has a body, so nothing can.
 */
export { Button, ButtonLink, IconButton, IconLink, buttonClass } from './button'
export type { ButtonLook, ButtonSize, ButtonTone, ButtonVariant } from './button'
export { Segmented, SubNav, WindowPicker } from './choice'
export { CodeViewer, type Lang } from './code'
export { ConfirmDialog, ConfirmProvider, useConfirm, type ConfirmRequest } from './confirm'
export { CONCEAL, CopyButton, CopyField, RevealButton, copyText, saveAs, useCopy } from './copy'
export { Chip, Figure, Figures, InputChip, KeyValue, Overline, Stat, StatGrid } from './data'
export { Banner, Empty, ErrorBanner, ErrorState, Freshness, Loading, Muted, Verbatim } from './feedback'
export { Field, Select, SettingSwitch, StepSlider, Switch, outlinedField } from './field'
export type { SelectOption } from './field'
export { Help, InfoTip, Note } from './info'
export { AppLink, InlineLink, appLinkClass, follow, isPlainLeftClick, openRow } from './link'
export { AddButton, ListRow, ListSummary, ListToolbar, RowBadge } from './list'
export { ContextMenu, MenuRow } from './menu'
export { MeterRow, UsageBar } from './meter'
export { ErrorModal, Modal } from './modal'
export { AppTitle, BrandMark, Breadcrumbs, NavRail, VersionTag, railItem } from './nav'
export { MenuDivider, Popover, menuItem, menuSurface, useAnchoredPanel } from './popover'
export type { Placement } from './popover'
export { Spinner } from './spinner'
export { Actions, Card, CardTitle, DangerZone, Page, Section, Stack, Tile, tile } from './surface'
export type { Gap } from './surface'
export { Table, Td, Tr, type Column } from './table'
export type { Tone } from '../styles/tone'
