import Ember from 'ember';
import config from '../config/environment';

function numberOrZero(value) {
  var number = Number(value);

  if (!isFinite(number)) {
    return 0;
  }

  return number;
}

export default Ember.Controller.extend({
  get config() {
    return config.APP;
  },

  height: Ember.computed('bestNode.height', {
    get() {
      var node = this.get('bestNode');
      if (node) {
        return node.height;
      }
      return 0;
    }
  }),

  roundShares: Ember.computed('model.stats', {
    get() {
      return parseInt(this.get('model.stats.roundShares'));
    }
  }),

  difficulty: Ember.computed('bestNode.difficulty', {
    get() {
      var node = this.get('bestNode');
      if (node) {
        return node.difficulty;
      }
      return 0;
    }
  }),

  hashrate: Ember.computed('difficulty', {
    get() {
      return numberOrZero(this.get('difficulty')) / config.APP.BlockTime;
    }
  }),

  immatureTotal: Ember.computed('model', {
    get() {
      return this.getWithDefault('model.immatureTotal', 0) + this.getWithDefault('model.candidatesTotal', 0);
    }
  }),

  bestNode: Ember.computed('model.nodes.@each.height', {
    get() {
      var node = null;
      var nodes = this.get('model.nodes') || [];

      nodes.forEach(function (n) {
        if (!node) {
          node = n;
        }
        if (numberOrZero(node.height) < numberOrZero(n.height)) {
          node = n;
        }
      });
      return node;
    }
  }),

  lastBlockFound: Ember.computed('model', {
    get() {
      return parseInt(this.get('model.lastBlockFound')) || 0;
    }
  }),

  roundVariance: Ember.computed('model', {
    get() {
      var percent = this.get('model.stats.roundShares') / this.get('difficulty');
      if (!percent) {
        return 0;
      }
      return percent.toFixed(2);
    }
  }),

  nextEpoch: Ember.computed('height', {
    get() {
      var epochOffset = (30000 - (this.getWithDefault('height', 1) % 30000)) * 1000 * this.get('config').BlockTime;
      return Date.now() + epochOffset;
    }
  })
});
