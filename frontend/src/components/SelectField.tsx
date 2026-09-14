import { createListCollection, Portal, Select } from '@chakra-ui/react'
import type { ReactNode } from 'react'
import { useMemo } from 'react'

/** A single-choice select over a plain string value. */
export default function SelectField({
  label,
  value,
  onChange,
  items,
  flex,
}: {
  label: ReactNode
  value: string
  onChange: (value: string) => void
  items: { label: string; value: string }[]
  flex?: string
}) {
  const collection = useMemo(() => createListCollection({ items }), [items])

  return (
    <Select.Root
      collection={collection}
      value={[value]}
      onValueChange={(details) => onChange(details.value[0] ?? '')}
      flex={flex}
      minW="0"
    >
      <Select.HiddenSelect />
      <Select.Label>{label}</Select.Label>
      <Select.Control>
        <Select.Trigger>
          <Select.ValueText />
        </Select.Trigger>
        <Select.IndicatorGroup>
          <Select.Indicator />
        </Select.IndicatorGroup>
      </Select.Control>
      <Portal>
        <Select.Positioner>
          <Select.Content>
            {collection.items.map((item) => (
              <Select.Item item={item} key={item.value}>
                <Select.ItemText>{item.label}</Select.ItemText>
                <Select.ItemIndicator />
              </Select.Item>
            ))}
          </Select.Content>
        </Select.Positioner>
      </Portal>
    </Select.Root>
  )
}
