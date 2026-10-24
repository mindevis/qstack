import { defineEventHandler } from 'nuxt/server'

const members = [{
  name: 'Anthony Fu',
  username: 'antfu',
  role: 'member',
  avatar: {}
}, {
  name: 'Baptiste Leproux',
  username: 'larbish',
  role: 'member',
  avatar: {}
}, {
  name: 'Benjamin Canac',
  username: 'benjamincanac',
  role: 'owner',
  avatar: {}
}, {
  name: 'Céline Dumerc',
  username: 'celinedumerc',
  role: 'member',
  avatar: {}
}, {
  name: 'Daniel Roe',
  username: 'danielroe',
  role: 'member',
  avatar: {}
}, {
  name: 'Farnabaz',
  username: 'farnabaz',
  role: 'member',
  avatar: {}
}, {
  name: 'Ferdinand Coumau',
  username: 'FerdinandCoumau',
  role: 'member',
  avatar: {}
}, {
  name: 'Hugo Richard',
  username: 'hugorcd',
  role: 'owner',
  avatar: {}
}, {
  name: 'Pooya Parsa',
  username: 'pi0',
  role: 'member',
  avatar: {}
}, {
  name: 'Sarah Moriceau',
  username: 'SarahM19',
  role: 'member',
  avatar: {}
}, {
  name: 'Sébastien Chopin',
  username: 'Atinux',
  role: 'owner',
  avatar: {}
}]

export default defineEventHandler(async () => {
  return members
})
