// The website uses the same XEU artwork as the desktop packages.
export function AppIcon(props: { size?: number; class?: string }) {
  const size = props.size ?? 32
  return <img class={props.class} src="/icon.png" width={size} height={size} alt="" aria-hidden="true" />
}
