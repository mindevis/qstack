import { defineEventHandler } from 'nuxt/server'
import { sub } from 'date-fns'

const notifications = [{
  id: 1,
  unread: true,
  sender: {
    name: 'Jordan Brown',
    email: 'jordan.brown@example.com',
    avatar: {
    }
  },
  body: 'sent you a message',
  date: sub(new Date(), { minutes: 7 }).toISOString()
}, {
  id: 2,
  sender: {
    name: 'Lindsay Walton'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { hours: 1 }).toISOString()
}, {
  id: 3,
  unread: true,
  sender: {
    name: 'Taylor Green',
    email: 'taylor.green@example.com',
    avatar: {
    }
  },
  body: 'sent you a message',
  date: sub(new Date(), { hours: 3 }).toISOString()
}, {
  id: 4,
  sender: {
    name: 'Courtney Henry',
    avatar: {
    }
  },
  body: 'added you to a project',
  date: sub(new Date(), { hours: 3 }).toISOString()
}, {
  id: 5,
  sender: {
    name: 'Tom Cook',
    avatar: {
    }
  },
  body: 'abandonned cart',
  date: sub(new Date(), { hours: 7 }).toISOString()
}, {
  id: 6,
  sender: {
    name: 'Casey Thomas',
    avatar: {
    }
  },
  body: 'purchased your product',
  date: sub(new Date(), { days: 1, hours: 3 }).toISOString()
}, {
  id: 7,
  unread: true,
  sender: {
    name: 'Kelly Wilson',
    email: 'kelly.wilson@example.com',
    avatar: {
    }
  },
  body: 'sent you a message',
  date: sub(new Date(), { days: 2 }).toISOString()
}, {
  id: 8,
  sender: {
    name: 'Jamie Johnson',
    email: 'jamie.johnson@example.com',
    avatar: {
    }
  },
  body: 'requested a refund',
  date: sub(new Date(), { days: 5, hours: 4 }).toISOString()
}, {
  id: 9,
  unread: true,
  sender: {
    name: 'Morgan Anderson',
    email: 'morgan.anderson@example.com'
  },
  body: 'sent you a message',
  date: sub(new Date(), { days: 6 }).toISOString()
}, {
  id: 10,
  sender: {
    name: 'Drew Moore'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 6 }).toISOString()
}, {
  id: 11,
  sender: {
    name: 'Riley Davis'
  },
  body: 'abandonned cart',
  date: sub(new Date(), { days: 7 }).toISOString()
}, {
  id: 12,
  sender: {
    name: 'Jordan Taylor'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 9 }).toISOString()
}, {
  id: 13,
  sender: {
    name: 'Kelly Wilson',
    email: 'kelly.wilson@example.com',
    avatar: {
    }
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 10 }).toISOString()
}, {
  id: 14,
  sender: {
    name: 'Jamie Johnson',
    email: 'jamie.johnson@example.com',
    avatar: {
    }
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 11 }).toISOString()
}, {
  id: 15,
  sender: {
    name: 'Morgan Anderson'
  },
  body: 'purchased your product',
  date: sub(new Date(), { days: 12 }).toISOString()
}, {
  id: 16,
  sender: {
    name: 'Drew Moore',
    avatar: {
    }
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 13 }).toISOString()
}, {
  id: 17,
  sender: {
    name: 'Riley Davis'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 14 }).toISOString()
}, {
  id: 18,
  sender: {
    name: 'Jordan Taylor'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 15 }).toISOString()
}, {
  id: 19,
  sender: {
    name: 'Kelly Wilson',
    email: 'kelly.wilson@example.com',
    avatar: {
    }
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 16 }).toISOString()
}, {
  id: 20,
  sender: {
    name: 'Jamie Johnson',
    email: 'jamie.johnson@example.com',
    avatar: {
    }
  },
  body: 'purchased your product',
  date: sub(new Date(), { days: 17 }).toISOString()
}, {
  id: 21,
  sender: {
    name: 'Morgan Anderson'
  },
  body: 'abandonned cart',
  date: sub(new Date(), { days: 17 }).toISOString()
}, {
  id: 22,
  sender: {
    name: 'Drew Moore'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 18 }).toISOString()
}, {
  id: 23,
  sender: {
    name: 'Riley Davis'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 19 }).toISOString()
}, {
  id: 24,
  sender: {
    name: 'Jordan Taylor',
    avatar: {
    }
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 20 }).toISOString()
}, {
  id: 25,
  sender: {
    name: 'Kelly Wilson',
    email: 'kelly.wilson@example.com',
    avatar: {
    }
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 20 }).toISOString()
}, {
  id: 26,
  sender: {
    name: 'Jamie Johnson',
    email: 'jamie.johnson@example.com',
    avatar: {
    }
  },
  body: 'abandonned cart',
  date: sub(new Date(), { days: 21 }).toISOString()
}, {
  id: 27,
  sender: {
    name: 'Morgan Anderson'
  },
  body: 'subscribed to your email list',
  date: sub(new Date(), { days: 22 }).toISOString()
}]

export default defineEventHandler(async () => {
  return notifications
})
